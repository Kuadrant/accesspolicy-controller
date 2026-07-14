package conformance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kube-agentic-networking/conformance"
	"sigs.k8s.io/kube-agentic-networking/pkg/infra/agentidentity/localca"
)

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

	// Background goroutine to fix conformance-tester deployment
	// since Kind/apiserver doesn't natively support podCertificate volume projection
	go func() {
		// Generate client certificate for conformance-tester-sa
		clientPubKey, clientPrivKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return
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
			return
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
		privKeyBytes, _ := x509.MarshalPKCS8PrivateKey(clientPrivKey)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privKeyBytes})
		caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.RootCertificate.Raw})

		for {
			time.Sleep(1 * time.Second)

			// Ensure tester namespace exists before secret creation
			_, err = opts.Clientset.CoreV1().Namespaces().Get(context.Background(), "agentic-conformance-infra", metav1.GetOptions{})
			if err != nil {
				continue
			}

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
				opts.Clientset.CoreV1().Secrets("agentic-conformance-infra").Update(context.Background(), testerSecret, metav1.UpdateOptions{})
			}

			// We modified base.yaml.tmpl directly, so no need to patch the deployment!
		}
	}()

	conformance.RunConformanceWithOptions(t, opts)
}
