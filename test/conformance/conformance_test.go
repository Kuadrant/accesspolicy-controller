package conformance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io/fs"
	"math/big"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kube-agentic-networking/conformance"
	"sigs.k8s.io/kube-agentic-networking/pkg/infra/agentidentity/localca"
)

type filteredFS struct {
	baseFS fs.FS
}

func (f filteredFS) Open(name string) (fs.File, error) {
	if name == "resources/base.yaml.tmpl" || name == "conformance/resources/base.yaml.tmpl" {
		return nil, fs.ErrNotExist
	}
	return f.baseFS.Open(name)
}

const customBaseYAML = `---
apiVersion: v1
kind: Namespace
metadata:
  name: agentic-conformance-infra
---
apiVersion: v1
kind: Namespace
metadata:
  name: gateway-conformance-infra
---
apiVersion: v1
kind: Namespace
metadata:
  name: gateway-conformance-web-backend
---
apiVersion: v1
kind: Namespace
metadata:
  name: gateway-conformance-app-backend
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: conformance-tester-sa
  namespace: agentic-conformance-infra
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: conformance-tester
  namespace: agentic-conformance-infra
spec:
  replicas: 1
  selector:
    matchLabels:
      app: conformance-tester
  template:
    metadata:
      labels:
        app: conformance-tester
    spec:
      serviceAccountName: conformance-tester-sa
      containers:
      - name: tester
        image: curlimages/curl:latest
        command: ["sleep", "infinity"]
        volumeMounts:
        - name: agent-identity-mtls
          mountPath: /run/agent-identity-mtls
          readOnly: true
      volumes:
      - name: agent-identity-mtls
        secret:
          secretName: conformance-tester-mtls
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: conformance-mcp-backend
  namespace: agentic-conformance-infra
spec:
  replicas: 1
  selector:
    matchLabels:
      app: conformance-mcp-backend
  template:
    metadata:
      labels:
        app: conformance-mcp-backend
    spec:
      containers:
        - name: mcp-backend
          image: us-central1-docker.pkg.dev/k8s-staging-images/agentic-net/quickstart-everything-mcp:main
          ports:
            - containerPort: 3001
          env:
            - name: PORT
              value: "3001"
---
apiVersion: v1
kind: Service
metadata:
  name: conformance-mcp-backend-svc
  namespace: agentic-conformance-infra
spec:
  type: ClusterIP
  selector:
    app: conformance-mcp-backend
  ports:
    - port: 3001
      targetPort: 3001
      protocol: TCP
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: conformance-primary
  namespace: agentic-conformance-infra
spec:
  gatewayClassName: "{GATEWAY_CLASS_NAME}"
  listeners:
  - name: http
    port: 80
    protocol: HTTP
    allowedRoutes:
      namespaces:
        from: All
  - name: https
    port: 443
    protocol: HTTPS
    allowedRoutes:
      namespaces:
        from: All
    tls:
      mode: Terminate
      certificateRefs:
      - name: gateway-server-cert
        kind: Secret
        group: ""
  tls:
    frontend:
      default:
        validation:
          caCertificateRefs:
          - name: gateway-client-ca
            kind: ConfigMap
            group: ""
`

func TestConformance(t *testing.T) {
	opts := conformance.DefaultOptions(t)

	// Pre-create the agentic-identity-ca-pool secret which is required by conformance tests
	ca, err := localca.GenerateED25519CA("default")
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	pool := &localca.Pool{
		CAs: []*localca.CA{ca},
	}
	poolBytes, err := localca.Marshal(pool)
	if err != nil {
		t.Fatalf("failed to marshal CA pool: %v", err)
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agentic-net-system",
		},
	}
	_, err = opts.Clientset.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("failed to create namespace: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agentic-identity-ca-pool",
			Namespace: "agentic-net-system",
		},
		Data: map[string][]byte{
			"ca-pool.json": poolBytes,
		},
	}
	_, err = opts.Clientset.CoreV1().Secrets("agentic-net-system").Create(context.Background(), secret, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		_, err = opts.Clientset.CoreV1().Secrets("agentic-net-system").Update(context.Background(), secret, metav1.UpdateOptions{})
		if err != nil {
			t.Fatalf("failed to update secret: %v", err)
		}
	}

	// Pre-create agentic-conformance-infra namespace & secret
	infraNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agentic-conformance-infra",
		},
	}
	_, _ = opts.Clientset.CoreV1().Namespaces().Create(context.Background(), infraNs, metav1.CreateOptions{})

	clientPubKey, clientPrivKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, _ := rand.Int(rand.Reader, serialNumberLimit)
	clientTemplate := &x509.Certificate{
		SerialNumber:          serialNumber,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		Subject:               pkix.Name{CommonName: "spiffe://cluster.local/ns/agentic-conformance-infra/sa/conformance-tester-sa"},
		URIs:                  []*url.URL{{Scheme: "spiffe", Host: "cluster.local", Path: "/ns/agentic-conformance-infra/sa/conformance-tester-sa"}},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, ca.RootCertificate, clientPubKey, ca.SigningKey)
	if err != nil {
		t.Fatalf("failed to create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	privKeyBytes, _ := x509.MarshalPKCS8PrivateKey(clientPrivKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privKeyBytes})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.RootCertificate.Raw})

	testerSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "conformance-tester-mtls",
			Namespace: "agentic-conformance-infra",
		},
		Data: map[string][]byte{
			"credential-bundle.pem":          append(certPEM, keyPEM...),
			"cluster.local.trust-bundle.pem": caPEM,
		},
	}
	_, err = opts.Clientset.CoreV1().Secrets("agentic-conformance-infra").Create(context.Background(), testerSecret, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		_, _ = opts.Clientset.CoreV1().Secrets("agentic-conformance-infra").Update(context.Background(), testerSecret, metav1.UpdateOptions{})
	}

	// Override base.yaml.tmpl in opts.ManifestFS to mount conformance-tester-mtls secret
	mapFS := fstest.MapFS{
		"resources/base.yaml.tmpl": &fstest.MapFile{Data: []byte(customBaseYAML)},
	}
	opts.ManifestFS = []fs.FS{mapFS, filteredFS{baseFS: &conformance.Manifests}}

	// Background goroutine to resolve pending LoadBalancer external IPs
	go func() {
		for {
			time.Sleep(1 * time.Second)
			svcs, err := opts.Clientset.CoreV1().Services("agentic-conformance-infra").List(context.Background(), metav1.ListOptions{})
			if err == nil {
				for _, svc := range svcs.Items {
					if svc.Spec.Type == corev1.ServiceTypeLoadBalancer && len(svc.Status.LoadBalancer.Ingress) == 0 {
						svcCopy := svc.DeepCopy()
						svcCopy.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: svcCopy.Spec.ClusterIP}}
						_, _ = opts.Clientset.CoreV1().Services("agentic-conformance-infra").UpdateStatus(context.Background(), svcCopy, metav1.UpdateOptions{})
					}
				}
			}
		}
	}()

	conformance.RunConformanceWithOptions(t, opts)
}
