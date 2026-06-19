#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# accesspolicy-controller quickstart
# Sets up a complete demo environment with Kind, Kuadrant, MCP server & agent
# ============================================================================

# ── Colors & formatting ─────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
RESET='\033[0m'

STEP=0

step() {
  STEP=$((STEP + 1))
  echo ""
  echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  echo -e "${BOLD}${CYAN}  Step ${STEP}: ${1}${RESET}"
  echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
}

info()    { echo -e "  ${DIM}ℹ${RESET}  ${1}"; }
success() { echo -e "  ${GREEN}✔${RESET}  ${1}"; }
warn()    { echo -e "  ${YELLOW}⚠${RESET}  ${1}"; }
fail()    { echo -e "  ${RED}✖${RESET}  ${1}"; exit 1; }

# ── Configuration ────────────────────────────────────────────────────────────
CLUSTER_NAME="accesspolicy-demo"
CONTROLLER_IMG="accesspolicy-controller:demo"
MCP_SERVER_IMG="accesspolicy-mcp-server:demo"
AGENT_IMG="accesspolicy-agent:demo"
NAMESPACE="quickstart-ns"
GATEWAY_API_VERSION="v1.2.0"
PORT_FORWARD_PORT=8081

# Resolve project root (the directory containing this script's parent)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ─────────────────────────────────────────────────────────────────────────────
# Step 1: Check prerequisites
# ─────────────────────────────────────────────────────────────────────────────
step "Checking prerequisites"

MISSING=()
for cmd in kind kubectl docker go helm; do
  if command -v "${cmd}" &>/dev/null; then
    success "${cmd} $(${cmd} version --short 2>/dev/null || ${cmd} version --client 2>/dev/null | head -1 || echo 'found')"
  else
    MISSING+=("${cmd}")
    warn "${cmd} — ${RED}not found${RESET}"
  fi
done

if [[ ${#MISSING[@]} -gt 0 ]]; then
  fail "Missing required tools: ${MISSING[*]}. Please install them and re-run."
fi

if [[ -z "${GOOGLE_API_KEY:-}" ]]; then
  fail "GOOGLE_API_KEY environment variable is not set.\n  Export it before running: ${BOLD}export GOOGLE_API_KEY=your-key${RESET}"
fi
success "GOOGLE_API_KEY is set"

# ─────────────────────────────────────────────────────────────────────────────
# Step 2: Create Kind cluster
# ─────────────────────────────────────────────────────────────────────────────
step "Creating Kind cluster '${CLUSTER_NAME}'"

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  warn "Cluster '${CLUSTER_NAME}' already exists — skipping creation"
else
  kind create cluster --config "${SCRIPT_DIR}/kind-config.yaml"
  success "Kind cluster '${CLUSTER_NAME}' created"
fi

kubectl cluster-info --context "kind-${CLUSTER_NAME}" >/dev/null 2>&1
success "kubectl context set to kind-${CLUSTER_NAME}"

# ─────────────────────────────────────────────────────────────────────────────
# Step 3: Install Gateway API CRDs
# ─────────────────────────────────────────────────────────────────────────────
step "Installing Gateway API CRDs (${GATEWAY_API_VERSION})"

kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml" \
  --context "kind-${CLUSTER_NAME}"
success "Gateway API CRDs installed"

# ─────────────────────────────────────────────────────────────────────────────
# Step 4: Install Kuadrant operator
# ─────────────────────────────────────────────────────────────────────────────
step "Installing Kuadrant operator via Helm"

helm repo add kuadrant https://kuadrant.io/helm-charts/ 2>/dev/null || true
helm repo update kuadrant

if helm status kuadrant --namespace kuadrant-system --kube-context "kind-${CLUSTER_NAME}" &>/dev/null; then
  warn "Kuadrant already installed — skipping"
else
  helm install kuadrant kuadrant/kuadrant-operator \
    --namespace kuadrant-system \
    --create-namespace \
    --kube-context "kind-${CLUSTER_NAME}" \
    --wait --timeout 5m
  success "Kuadrant operator installed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 5: Build & load controller image
# ─────────────────────────────────────────────────────────────────────────────
step "Building controller image (${CONTROLLER_IMG})"

cd "${PROJECT_ROOT}"
make docker-build IMG="${CONTROLLER_IMG}"
kind load docker-image "${CONTROLLER_IMG}" --name "${CLUSTER_NAME}"
success "Controller image built and loaded into Kind"

# ─────────────────────────────────────────────────────────────────────────────
# Step 6: Install XAccessPolicy CRDs
# ─────────────────────────────────────────────────────────────────────────────
step "Installing XAccessPolicy CRDs"

cd "${PROJECT_ROOT}"
make install
success "XAccessPolicy CRDs installed"

# ─────────────────────────────────────────────────────────────────────────────
# Step 7: Deploy the controller
# ─────────────────────────────────────────────────────────────────────────────
step "Deploying accesspolicy-controller"

cd "${PROJECT_ROOT}"
make deploy IMG="${CONTROLLER_IMG}"
info "Waiting for controller deployment to be ready..."
kubectl rollout status deployment/accesspolicy-controller-manager \
  -n accesspolicy-system \
  --context "kind-${CLUSTER_NAME}" \
  --timeout=120s
success "Controller deployed and ready"

# ─────────────────────────────────────────────────────────────────────────────
# Step 8: Create quickstart namespace
# ─────────────────────────────────────────────────────────────────────────────
step "Creating namespace '${NAMESPACE}'"

kubectl create namespace "${NAMESPACE}" --context "kind-${CLUSTER_NAME}" --dry-run=client -o yaml \
  | kubectl apply -f - --context "kind-${CLUSTER_NAME}"
success "Namespace '${NAMESPACE}' ready"

# ─────────────────────────────────────────────────────────────────────────────
# Step 9: Build & load MCP server image
# ─────────────────────────────────────────────────────────────────────────────
step "Building MCP server image (${MCP_SERVER_IMG})"

docker build -t "${MCP_SERVER_IMG}" "${SCRIPT_DIR}/mcpserver"
kind load docker-image "${MCP_SERVER_IMG}" --name "${CLUSTER_NAME}"
success "MCP server image built and loaded into Kind"

# ─────────────────────────────────────────────────────────────────────────────
# Step 10: Deploy MCP server
# ─────────────────────────────────────────────────────────────────────────────
step "Deploying MCP server"

kubectl apply -f "${SCRIPT_DIR}/mcpserver/deployment.yaml" --context "kind-${CLUSTER_NAME}"
info "Waiting for MCP server deployment to be ready..."
kubectl rollout status deployment/mcp-server \
  -n "${NAMESPACE}" \
  --context "kind-${CLUSTER_NAME}" \
  --timeout=120s
success "MCP server deployed and ready"

# ─────────────────────────────────────────────────────────────────────────────
# Step 11: Build & load agent image
# ─────────────────────────────────────────────────────────────────────────────
step "Building agent image (${AGENT_IMG})"

docker build -t "${AGENT_IMG}" "${SCRIPT_DIR}/agent"
kind load docker-image "${AGENT_IMG}" --name "${CLUSTER_NAME}"
success "Agent image built and loaded into Kind"

# ─────────────────────────────────────────────────────────────────────────────
# Step 12: Apply policy resources (Gateway + XAccessPolicy)
# ─────────────────────────────────────────────────────────────────────────────
step "Applying Gateway & XAccessPolicy"

kubectl apply -f "${SCRIPT_DIR}/policy/resources.yaml" --context "kind-${CLUSTER_NAME}"
success "Gateway and XAccessPolicy applied"

info "Waiting for XAccessPolicy to be accepted..."
sleep 3
kubectl get xaccesspolicies -n "${NAMESPACE}" --context "kind-${CLUSTER_NAME}" || true

# ─────────────────────────────────────────────────────────────────────────────
# Step 13: Deploy agent
# ─────────────────────────────────────────────────────────────────────────────
step "Deploying agent"

kubectl create secret generic google-api-key \
  --from-literal=api-key="${GOOGLE_API_KEY}" \
  -n "${NAMESPACE}" \
  --context "kind-${CLUSTER_NAME}" \
  --dry-run=client -o yaml \
  | kubectl apply -f - --context "kind-${CLUSTER_NAME}"
success "Google API key secret created"

kubectl apply -f "${SCRIPT_DIR}/agent/deployment.yaml" --context "kind-${CLUSTER_NAME}"
info "Waiting for agent deployment to be ready..."
kubectl rollout status deployment/mcp-agent \
  -n "${NAMESPACE}" \
  --context "kind-${CLUSTER_NAME}" \
  --timeout=120s
success "Agent deployed and ready"

# ─────────────────────────────────────────────────────────────────────────────
# Step 14: Port-forward agent service
# ─────────────────────────────────────────────────────────────────────────────
step "Setting up port-forward (localhost:${PORT_FORWARD_PORT} → agent:80)"

# Kill any existing port-forward on the same port
lsof -ti ":${PORT_FORWARD_PORT}" 2>/dev/null | xargs -r kill 2>/dev/null || true

kubectl port-forward svc/mcp-agent "${PORT_FORWARD_PORT}:80" \
  -n "${NAMESPACE}" \
  --context "kind-${CLUSTER_NAME}" &
PF_PID=$!
sleep 2

if kill -0 "${PF_PID}" 2>/dev/null; then
  success "Port-forward active (PID ${PF_PID})"
else
  warn "Port-forward may have failed — check manually"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 15: Print instructions
# ─────────────────────────────────────────────────────────────────────────────
step "Quickstart ready!"

echo ""
echo -e "${GREEN}${BOLD}  ✅  accesspolicy-controller demo is running!${RESET}"
echo ""
echo -e "  ${BOLD}Agent UI:${RESET}  ${CYAN}http://localhost:${PORT_FORWARD_PORT}${RESET}"
echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo -e "${BOLD}  Try these prompts in the agent UI:${RESET}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo ""
printf "  ${BOLD}%-40s %-20s %-15s${RESET}\n" "Prompt" "Tool" "Expected"
printf "  ${DIM}%-40s %-20s %-15s${RESET}\n" "────────────────────────────────────────" "────────────────────" "───────────────"
printf "  %-40s %-20s ${GREEN}%-15s${RESET}\n" "What is the sum of 2 and 3?" "get-sum" "✅ Allowed"
printf "  %-40s %-20s ${GREEN}%-15s${RESET}\n" "Echo back hello" "echo" "✅ Allowed"
printf "  %-40s %-20s ${RED}%-15s${RESET}\n" "Get me a tiny image" "get-tiny-image" "❌ Blocked"
echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo -e "  ${BOLD}Dynamic policy update:${RESET}"
echo -e "  ${CYAN}kubectl apply -f quickstart/policy/updated-policy.yaml${RESET}"
echo ""
echo -e "  This swaps ${YELLOW}echo${RESET} → ${YELLOW}get-tiny-image${RESET} in the allow list."
echo -e "  After applying, ${GREEN}get-tiny-image${RESET} will be ${GREEN}✅ Allowed${RESET}"
echo -e "  and ${RED}echo${RESET} will be ${RED}❌ Blocked${RESET}."
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo ""
echo -e "  ${DIM}Cleanup: kind delete cluster --name ${CLUSTER_NAME}${RESET}"
echo -e "  ${DIM}Logs:    kubectl logs -n accesspolicy-system deployment/accesspolicy-controller-manager -c manager -f${RESET}"
echo ""
