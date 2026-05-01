#!/usr/bin/env bash
# =============================================================================
# GoSentinel — Minikube deployment script
# Starts Minikube, builds images inside Minikube's Docker daemon, and deploys
# the full LGTM stack + GoSentinel application.
#
# Usage:
#   ./scripts/minikube-up.sh [--skip-build] [--cpus N] [--memory M]
#
# Requirements:
#   - minikube >= 1.30
#   - kubectl
#   - docker
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── Defaults ──────────────────────────────────────────────────────────────────
MINIKUBE_CPUS="${MINIKUBE_CPUS:-4}"
MINIKUBE_MEMORY="${MINIKUBE_MEMORY:-6144}"   # 6 GB — LGTM stack needs headroom
MINIKUBE_DISK="${MINIKUBE_DISK:-30g}"
MINIKUBE_DRIVER="${MINIKUBE_DRIVER:-docker}"
IMAGE_TAG="dev"
SKIP_BUILD=false
NAMESPACE="gosentinel"

# ── Argument parsing ──────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-build) SKIP_BUILD=true; shift ;;
    --cpus)       MINIKUBE_CPUS="$2"; shift 2 ;;
    --memory)     MINIKUBE_MEMORY="$2"; shift 2 ;;
    --driver)     MINIKUBE_DRIVER="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# ── Colours ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error()   { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }
step()    { echo -e "\n${CYAN}══════════════════════════════════════════${NC}"; \
            echo -e "${CYAN}  $*${NC}"; \
            echo -e "${CYAN}══════════════════════════════════════════${NC}"; }

# ── Preflight checks ──────────────────────────────────────────────────────────
step "Preflight checks"

command -v minikube >/dev/null 2>&1 || error "minikube not found. Install from https://minikube.sigs.k8s.io/docs/start/"
command -v kubectl  >/dev/null 2>&1 || error "kubectl not found."
command -v docker   >/dev/null 2>&1 || error "docker not found."

info "minikube: $(minikube version --short 2>/dev/null || minikube version | head -1)"
info "kubectl:  $(kubectl version --client --short 2>/dev/null || kubectl version --client | head -1)"
info "docker:   $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo 'unknown')"

# ── Start Minikube ────────────────────────────────────────────────────────────
step "Starting Minikube (cpus=$MINIKUBE_CPUS, memory=${MINIKUBE_MEMORY}MB, driver=$MINIKUBE_DRIVER)"

MINIKUBE_STATUS=$(minikube status --format='{{.Host}}' 2>/dev/null || echo "Stopped")

if [[ "$MINIKUBE_STATUS" == "Running" ]]; then
  success "Minikube already running"
else
  info "Starting Minikube..."
  minikube start \
    --cpus="$MINIKUBE_CPUS" \
    --memory="$MINIKUBE_MEMORY" \
    --disk-size="$MINIKUBE_DISK" \
    --driver="$MINIKUBE_DRIVER" \
    --kubernetes-version=stable \
    --addons=metrics-server \
    --addons=storage-provisioner \
    --wait=all \
    --wait-timeout=5m
  success "Minikube started"
fi

# Enable ingress addon (optional, for browser access)
minikube addons enable ingress 2>/dev/null || warn "ingress addon not enabled (optional)"

# ── Build images inside Minikube ──────────────────────────────────────────────
if [[ "$SKIP_BUILD" == "false" ]]; then
  step "Building Docker images inside Minikube's Docker daemon"
  info "Pointing Docker CLI to Minikube's daemon..."

  # Use minikube's docker daemon so images are available without a registry
  eval "$(minikube docker-env)"

  for svc in pipeline api ui; do
    info "Building gosentinel/$svc:$IMAGE_TAG ..."
    docker build \
      -t "gosentinel/$svc:$IMAGE_TAG" \
      -f "$PROJECT_DIR/deploy/docker/$svc.Dockerfile" \
      "$PROJECT_DIR"
    success "Built gosentinel/$svc:$IMAGE_TAG"
  done

  # Reset Docker env back to host
  eval "$(minikube docker-env --unset)"
else
  warn "Skipping image build (--skip-build)"
fi

# ── Deploy manifests ──────────────────────────────────────────────────────────
step "Deploying to Minikube"

MANIFESTS_DIR="$PROJECT_DIR/deploy/k8s/minikube"

info "Creating namespace..."
kubectl apply -f "$MANIFESTS_DIR/00-namespace.yaml"

info "Applying ConfigMaps and Secrets..."
kubectl apply -f "$MANIFESTS_DIR/01-configmap.yaml"

info "Deploying PostgreSQL..."
kubectl apply -f "$MANIFESTS_DIR/02-postgres.yaml"

info "Deploying LGTM stack (Loki, Grafana, Tempo, Mimir, VictoriaMetrics, Jaeger, Pyroscope)..."
kubectl apply -f "$MANIFESTS_DIR/03-lgtm-stack.yaml"

info "Deploying OpenTelemetry Collector..."
kubectl apply -f "$MANIFESTS_DIR/04-otel-collector.yaml"

info "Applying Grafana dashboard..."
kubectl apply -f "$MANIFESTS_DIR/06-grafana-dashboard.yaml"

# ── Wait for backends ─────────────────────────────────────────────────────────
step "Waiting for backend services to be ready"

wait_for_deployment() {
  local name="$1"
  local timeout="${2:-180}"
  info "Waiting for $name (timeout: ${timeout}s)..."
  kubectl rollout status deployment/"$name" -n "$NAMESPACE" --timeout="${timeout}s" \
    && success "$name is ready" \
    || warn "$name did not become ready in time — check: kubectl logs -n $NAMESPACE deployment/$name"
}

wait_for_deployment postgres 120
wait_for_deployment victoriametrics 120
wait_for_deployment loki 180
wait_for_deployment tempo 180
wait_for_deployment mimir 240
wait_for_deployment jaeger 120
wait_for_deployment pyroscope 120
wait_for_deployment otel-collector 120
wait_for_deployment grafana 180

# ── Deploy GoSentinel ─────────────────────────────────────────────────────────
step "Deploying GoSentinel application"

kubectl apply -f "$MANIFESTS_DIR/05-gosentinel.yaml"

wait_for_deployment gosentinel-pipeline 180
wait_for_deployment gosentinel-api 120
wait_for_deployment gosentinel-ui 120

# ── Print access URLs ─────────────────────────────────────────────────────────
step "Deployment complete!"

MINIKUBE_IP=$(minikube ip)

echo ""
echo -e "${GREEN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║              GoSentinel on Minikube — Access URLs            ║${NC}"
echo -e "${GREEN}╠══════════════════════════════════════════════════════════════╣${NC}"

print_url() {
  local name="$1"
  local svc="$2"
  local port="$3"
  local ns="${4:-$NAMESPACE}"
  local node_port
  node_port=$(kubectl get svc "$svc" -n "$ns" -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || echo "N/A")
  if [[ "$node_port" != "N/A" ]]; then
    echo -e "${GREEN}║${NC}  ${CYAN}$name${NC}"
    echo -e "${GREEN}║${NC}    minikube service: $(minikube service "$svc" -n "$ns" --url 2>/dev/null || echo "http://$MINIKUBE_IP:$node_port")"
  fi
}

echo ""
echo -e "  ${CYAN}GoSentinel UI${NC}      → $(minikube service gosentinel-ui -n $NAMESPACE --url 2>/dev/null || echo 'run: minikube service gosentinel-ui -n gosentinel --url')"
echo -e "  ${CYAN}GoSentinel API${NC}     → $(minikube service gosentinel-api -n $NAMESPACE --url 2>/dev/null || echo 'run: minikube service gosentinel-api -n gosentinel --url')"
echo -e "  ${CYAN}Grafana${NC}            → $(minikube service grafana -n $NAMESPACE --url 2>/dev/null || echo 'run: minikube service grafana -n gosentinel --url')  (admin/gosentinel)"
echo -e "  ${CYAN}Jaeger UI${NC}          → $(minikube service jaeger -n $NAMESPACE --url 2>/dev/null | head -1 || echo 'run: minikube service jaeger -n gosentinel --url')"
echo ""
echo -e "  ${YELLOW}Quick access (opens browser):${NC}"
echo -e "    minikube service grafana -n gosentinel"
echo -e "    minikube service gosentinel-ui -n gosentinel"
echo ""
echo -e "  ${YELLOW}Port-forward alternatives:${NC}"
echo -e "    kubectl port-forward svc/grafana 3001:3000 -n gosentinel"
echo -e "    kubectl port-forward svc/gosentinel-ui 3000:3000 -n gosentinel"
echo -e "    kubectl port-forward svc/gosentinel-api 8080:8080 -n gosentinel"
echo -e "    kubectl port-forward svc/tempo 3200:3200 -n gosentinel"
echo -e "    kubectl port-forward svc/mimir 9009:9009 -n gosentinel"
echo ""
echo -e "  ${YELLOW}Test alert notification:${NC}"
echo -e "    API_URL=\$(minikube service gosentinel-api -n gosentinel --url)"
echo -e "    curl -X POST \$API_URL/api/v1/alerts/test \\"
echo -e "      -H 'Content-Type: application/json' \\"
echo -e "      -d '{\"channel\":\"slack\",\"severity\":\"warning\",\"summary\":\"Test from Minikube\"}'"
echo ""
echo -e "  ${YELLOW}View logs:${NC}"
echo -e "    kubectl logs -f deployment/gosentinel-pipeline -n gosentinel"
echo -e "    kubectl logs -f deployment/otel-collector -n gosentinel"
echo ""
echo -e "${GREEN}╚══════════════════════════════════════════════════════════════╝${NC}"
