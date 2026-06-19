# Demo Walkthrough: XAccessPolicy to AuthPolicy Standalone Controller

## Objective
Demonstrate how the standalone controller converts an `XAccessPolicy` targeting a `Gateway` into a Kuadrant `AuthPolicy`, mapping native CEL expressions directly to dynamically enforce tool-level access on `mcp-gateway`.

## 1. Local Cluster Setup
Spin up a local `kind` cluster and install the necessary dependencies:

```bash
# Create cluster
kind create cluster --name accesspolicy-demo

# Install Gateway API
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml

# Install Kuadrant Operator (provides AuthPolicy CRD and Authorino)
helm repo add kuadrant https://kuadrant.io/helm-charts
helm install kuadrant-operator kuadrant/kuadrant-operator --namespace kuadrant-system --create-namespace

# Deploy MCP Gateway
helm install mcp-gateway kuadrant/mcp-gateway --namespace mcp-system --create-namespace
```

## 2. Deploy the AccessPolicy Controller
Build and load the controller image:

```bash
make install # Installs the XAccessPolicy CRDs
make docker-build IMG=accesspolicy-controller:demo
kind load docker-image accesspolicy-controller:demo --name accesspolicy-demo
make deploy IMG=accesspolicy-controller:demo
```

## 3. Apply the Infrastructure (Gateway & Backend)
We will define a generic Gateway that uses `mcp-gateway` as its class.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: prod-mcp-gateway
  namespace: default
spec:
  gatewayClassName: mcp-gateway
  listeners:
    - name: http
      protocol: HTTP
      port: 8080
EOF
```

## 4. Apply the XAccessPolicy
Apply an `XAccessPolicy` that allows only the `search_web` tool using a CEL expression.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: agentic.networking.x-k8s.io/v1alpha1
kind: XAccessPolicy
metadata:
  name: web-search-policy
  namespace: default
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: prod-mcp-gateway
  rules:
    - name: allow-search-web-only
      authorization:
        type: CEL
        cel:
          expression: "request.mcp.tool_name == 'search_web'"
EOF
```

## 5. Observe the Generated AuthPolicy
The controller will intercept the `XAccessPolicy` and generate a Kuadrant `AuthPolicy`, transferring the CEL expression directly to a pattern matching rule.

```bash
kubectl get authpolicy prod-mcp-gateway-auth -o yaml
```
*Notice how the CEL expression maps exactly to `spec.authScheme.authorization["combined-rules"].patternMatching.patterns[0].predicate`.*

## 6. Verification
Port-forward the Gateway and run some test requests. `mcp-gateway` parses the JSON body and extracts the tool name, allowing Authorino to evaluate the CEL predicate.

**Allowed Request:**
```bash
kubectl port-forward svc/mcp-gateway-prod-mcp-gateway 8080:8080 &

curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"method": "tools/call", "params": {"name": "search_web", "arguments": {"query": "kubernetes"}}}'
# Output: HTTP 200 OK (Assuming upstream mock is ready)
```

**Denied Request:**
```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"method": "tools/call", "params": {"name": "delete_database", "arguments": {}}}'
# Output: HTTP 403 Forbidden (Blocked by Authorino based on the generated AuthPolicy CEL rule)
```
