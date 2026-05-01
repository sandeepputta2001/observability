################################################################################
# GoSentinel — Makefile
# Usage: make help
################################################################################

BINARIES         := collector pipeline api ui
BUILD_DIR        := bin
MODULE           := github.com/yourorg/gosentinel
IMAGE_REPO       ?= gosentinel
IMAGE_TAG        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
AWS_REGION       ?= us-east-1
CLUSTER          ?= gosentinel-prod
TF_ENV           ?= dev
NAMESPACE        ?= gosentinel

# Minikube resource defaults — override via env vars or make flags
MINIKUBE_CPUS    ?= 4
MINIKUBE_MEMORY  ?= 6144
MINIKUBE_DRIVER  ?= docker

.PHONY: \
	build build-linux \
	test test-short test-alerting coverage \
	lint fmt vet \
	proto-gen proto-lint \
	docker-up docker-down docker-logs docker-build docker-push \
	kubeconfig \
	k8s-apply k8s-delete k8s-status k8s-logs \
	helm-install helm-uninstall helm-lint helm-template \
	minikube-up minikube-up-skip-build minikube-down minikube-delete \
	minikube-status minikube-logs-pipeline minikube-logs-otel \
	minikube-logs-all minikube-grafana minikube-ui minikube-api \
	minikube-pf minikube-rebuild minikube-test-alert minikube-channels \
	minikube-silence minikube-routing \
	tf-init tf-plan tf-apply tf-destroy tf-fmt \
	db-migrate db-migrate-minikube \
	clean help

# ── Build ─────────────────────────────────────────────────────────────────────

## build: compile all cmd/ binaries into ./bin/
build:
	@mkdir -p $(BUILD_DIR)
	@for bin in $(BINARIES); do \
		echo "  Building $$bin..."; \
		go build -ldflags="-X main.version=$(IMAGE_TAG)" \
		         -o $(BUILD_DIR)/$$bin ./cmd/$$bin/; \
	done
	@echo "Build complete: $(BUILD_DIR)/"

## build-linux: cross-compile for linux/amd64 (for Docker)
build-linux:
	@mkdir -p $(BUILD_DIR)
	@for bin in $(BINARIES); do \
		echo "  Building $$bin (linux/amd64)..."; \
		GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -ldflags="-s -w -X main.version=$(IMAGE_TAG)" \
		         -o $(BUILD_DIR)/$$bin-linux ./cmd/$$bin/; \
	done

# ── Test ──────────────────────────────────────────────────────────────────────

## test: run all tests with race detector
test:
	go test -race -count=1 -timeout=120s ./...

## test-short: run tests without integration tests
test-short:
	go test -race -short -count=1 ./...

## test-alerting: run only alerting package tests
test-alerting:
	go test -race -count=1 -v ./internal/alerting/...

## coverage: generate HTML coverage report
coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# ── Lint ──────────────────────────────────────────────────────────────────────

## lint: run golangci-lint
lint:
	golangci-lint run --timeout=5m ./...

## fmt: format all Go files
fmt:
	gofmt -w -s .
	goimports -w .

## vet: run go vet
vet:
	go vet ./...

# ── Proto ─────────────────────────────────────────────────────────────────────

## proto-gen: generate Go code from proto files using buf
proto-gen:
	buf generate
	@echo "Proto generation complete"

## proto-lint: lint proto files
proto-lint:
	buf lint

# ── Docker (local dev) ────────────────────────────────────────────────────────

## docker-up: start all backend services and example services
docker-up:
	docker compose up -d
	@echo "Waiting for services to be healthy..."
	@sleep 5
	@docker compose ps

## docker-down: stop all services and remove volumes
docker-down:
	docker compose down -v

## docker-logs: tail logs from all services
docker-logs:
	docker compose logs -f

## docker-build: build all Docker images
docker-build:
	@for svc in pipeline api ui; do \
		echo "  Building $(IMAGE_REPO)/$$svc:$(IMAGE_TAG)..."; \
		docker build -t $(IMAGE_REPO)/$$svc:$(IMAGE_TAG) \
		             -f deploy/docker/$$svc.Dockerfile .; \
	done
	@for svc in order-service inventory-service payment-service; do \
		echo "  Building $(IMAGE_REPO)/$$svc:$(IMAGE_TAG)..."; \
		docker build -t $(IMAGE_REPO)/$$svc:$(IMAGE_TAG) \
		             -f examples/services/$$svc/Dockerfile .; \
	done

## docker-push: push images to ECR
docker-push: docker-build
	@ECR_REGISTRY=$$(aws ecr describe-repositories \
		--repository-names gosentinel/api \
		--region $(AWS_REGION) \
		--query 'repositories[0].repositoryUri' \
		--output text | cut -d/ -f1); \
	aws ecr get-login-password --region $(AWS_REGION) | \
		docker login --username AWS --password-stdin $$ECR_REGISTRY; \
	for svc in pipeline api ui order-service inventory-service payment-service; do \
		docker tag $(IMAGE_REPO)/$$svc:$(IMAGE_TAG) \
		           $$ECR_REGISTRY/gosentinel/$$svc:$(IMAGE_TAG); \
		docker push $$ECR_REGISTRY/gosentinel/$$svc:$(IMAGE_TAG); \
	done

# ── Kubernetes (production / EKS) ─────────────────────────────────────────────

## kubeconfig: update kubeconfig for the EKS cluster
kubeconfig:
	aws eks update-kubeconfig --region $(AWS_REGION) --name $(CLUSTER)

## k8s-apply: apply all production Kubernetes manifests
k8s-apply: kubeconfig
	kubectl apply -f deploy/k8s/namespace.yaml
	kubectl apply -f deploy/k8s/rbac/
	kubectl apply -f deploy/k8s/network/
	kubectl apply -f deploy/k8s/gosentinel/configmap.yaml
	kubectl apply -f deploy/k8s/gosentinel/
	kubectl apply -f deploy/k8s/monitoring/
	@echo "Waiting for rollout..."
	kubectl rollout status deployment/gosentinel-pipeline -n $(NAMESPACE) --timeout=120s
	kubectl rollout status deployment/gosentinel-api      -n $(NAMESPACE) --timeout=120s
	kubectl rollout status deployment/gosentinel-ui       -n $(NAMESPACE) --timeout=120s

## k8s-delete: delete all GoSentinel resources (keeps namespace)
k8s-delete: kubeconfig
	kubectl delete -f deploy/k8s/gosentinel/ --ignore-not-found
	kubectl delete -f deploy/k8s/monitoring/ --ignore-not-found

## k8s-status: show status of all GoSentinel pods
k8s-status: kubeconfig
	kubectl get pods,svc,hpa,pdb -n $(NAMESPACE)

## k8s-logs: tail logs from pipeline
k8s-logs: kubeconfig
	kubectl logs -f -l app.kubernetes.io/name=gosentinel-pipeline -n $(NAMESPACE) --all-containers

# ── Helm ──────────────────────────────────────────────────────────────────────

## helm-install: install GoSentinel via Helm
helm-install: kubeconfig
	helm upgrade --install gosentinel deploy/helm/gosentinel \
		--namespace $(NAMESPACE) \
		--create-namespace \
		--set pipeline.image.tag=$(IMAGE_TAG) \
		--set api.image.tag=$(IMAGE_TAG) \
		--set ui.image.tag=$(IMAGE_TAG) \
		--wait --timeout=5m

## helm-uninstall: uninstall GoSentinel Helm release
helm-uninstall: kubeconfig
	helm uninstall gosentinel --namespace $(NAMESPACE)

## helm-lint: lint the Helm chart
helm-lint:
	helm lint deploy/helm/gosentinel

## helm-template: render Helm templates to stdout (dry-run)
helm-template:
	helm template gosentinel deploy/helm/gosentinel \
		--namespace $(NAMESPACE) \
		--set pipeline.image.tag=$(IMAGE_TAG) \
		--set api.image.tag=$(IMAGE_TAG) \
		--set ui.image.tag=$(IMAGE_TAG)

# ── Minikube ──────────────────────────────────────────────────────────────────

## minikube-up: start Minikube and deploy full LGTM stack + GoSentinel
## Customise: MINIKUBE_CPUS=6 MINIKUBE_MEMORY=8192 MINIKUBE_DRIVER=docker make minikube-up
minikube-up:
	@bash scripts/minikube-up.sh \
		--cpus $(MINIKUBE_CPUS) \
		--memory $(MINIKUBE_MEMORY) \
		--driver $(MINIKUBE_DRIVER)

## minikube-up-skip-build: deploy to existing Minikube without rebuilding images
minikube-up-skip-build:
	@bash scripts/minikube-up.sh --skip-build \
		--cpus $(MINIKUBE_CPUS) \
		--memory $(MINIKUBE_MEMORY) \
		--driver $(MINIKUBE_DRIVER)

## minikube-down: delete GoSentinel namespace from Minikube (keeps cluster)
minikube-down:
	@bash scripts/minikube-down.sh

## minikube-delete: delete the entire Minikube cluster
minikube-delete:
	@bash scripts/minikube-down.sh --delete-cluster

## minikube-status: show all pods, services, and PVCs in gosentinel namespace
minikube-status:
	kubectl get pods,svc,pvc -n $(NAMESPACE) -o wide

## minikube-logs-pipeline: tail GoSentinel pipeline logs
minikube-logs-pipeline:
	kubectl logs -f deployment/gosentinel-pipeline -n $(NAMESPACE)

## minikube-logs-otel: tail OpenTelemetry Collector logs
minikube-logs-otel:
	kubectl logs -f deployment/otel-collector -n $(NAMESPACE)

## minikube-logs-all: tail logs from all GoSentinel components
minikube-logs-all:
	@echo "Tailing logs from pipeline, api, ui, otel-collector..."
	@kubectl logs -f deployment/gosentinel-pipeline -n $(NAMESPACE) --prefix &
	@kubectl logs -f deployment/gosentinel-api -n $(NAMESPACE) --prefix &
	@kubectl logs -f deployment/gosentinel-ui -n $(NAMESPACE) --prefix &
	@kubectl logs -f deployment/otel-collector -n $(NAMESPACE) --prefix &
	@wait

## minikube-grafana: open Grafana in browser (admin/gosentinel)
minikube-grafana:
	minikube service grafana -n $(NAMESPACE)

## minikube-ui: open GoSentinel UI in browser
minikube-ui:
	minikube service gosentinel-ui -n $(NAMESPACE)

## minikube-api: print GoSentinel API URL
minikube-api:
	@minikube service gosentinel-api -n $(NAMESPACE) --url

## minikube-pf: port-forward all services to localhost
## Services: Grafana :3001, UI :3000, API :8080, Jaeger :16686,
##           Tempo :3200, Mimir :9009, VM :8428, Loki :3100, Pyroscope :4040
minikube-pf:
	@echo "Port-forwarding all services to localhost..."
	@echo "  Grafana:          http://localhost:3001  (admin/gosentinel)"
	@echo "  GoSentinel UI:    http://localhost:3000"
	@echo "  GoSentinel API:   http://localhost:8080"
	@echo "  Jaeger UI:        http://localhost:16686"
	@echo "  Tempo:            http://localhost:3200"
	@echo "  Mimir:            http://localhost:9009"
	@echo "  VictoriaMetrics:  http://localhost:8428"
	@echo "  Loki:             http://localhost:3100"
	@echo "  Pyroscope:        http://localhost:4040"
	@echo ""
	@echo "Press Ctrl+C to stop all port-forwards"
	@kubectl port-forward svc/grafana 3001:3000 -n $(NAMESPACE) &
	@kubectl port-forward svc/gosentinel-ui 3000:3000 -n $(NAMESPACE) &
	@kubectl port-forward svc/gosentinel-api 8080:8080 -n $(NAMESPACE) &
	@kubectl port-forward svc/jaeger 16686:16686 -n $(NAMESPACE) &
	@kubectl port-forward svc/tempo 3200:3200 -n $(NAMESPACE) &
	@kubectl port-forward svc/mimir 9009:9009 -n $(NAMESPACE) &
	@kubectl port-forward svc/victoriametrics 8428:8428 -n $(NAMESPACE) &
	@kubectl port-forward svc/loki 3100:3100 -n $(NAMESPACE) &
	@kubectl port-forward svc/pyroscope 4040:4040 -n $(NAMESPACE) &
	@wait

## minikube-rebuild: rebuild GoSentinel images and restart deployments
minikube-rebuild:
	@echo "Building images inside Minikube's Docker daemon..."
	@eval $$(minikube docker-env) && \
		docker build -t gosentinel/pipeline:dev -f deploy/docker/pipeline.Dockerfile . && \
		docker build -t gosentinel/api:dev -f deploy/docker/api.Dockerfile . && \
		docker build -t gosentinel/ui:dev -f deploy/docker/ui.Dockerfile .
	@echo "Restarting deployments..."
	kubectl rollout restart deployment/gosentinel-pipeline deployment/gosentinel-api deployment/gosentinel-ui -n $(NAMESPACE)
	kubectl rollout status deployment/gosentinel-pipeline deployment/gosentinel-api deployment/gosentinel-ui -n $(NAMESPACE) --timeout=120s

## minikube-test-alert: send a test alert notification via the API
## Usage: make minikube-test-alert CHANNEL=slack
CHANNEL ?= slack
minikube-test-alert:
	@API_URL=$$(minikube service gosentinel-api -n $(NAMESPACE) --url 2>/dev/null | head -1); \
	echo "Sending test alert to $$API_URL (channel: $(CHANNEL))..."; \
	curl -s -X POST "$$API_URL/api/v1/alerts/test" \
		-H "Content-Type: application/json" \
		-d '{"channel":"$(CHANNEL)","severity":"warning","summary":"Test alert from Minikube — $(shell date)"}' \
		| python3 -m json.tool 2>/dev/null || echo "(no JSON output)"

## minikube-channels: list registered notification channels
minikube-channels:
	@API_URL=$$(minikube service gosentinel-api -n $(NAMESPACE) --url 2>/dev/null | head -1); \
	echo "Registered channels at $$API_URL:"; \
	curl -s "$$API_URL/api/v1/channels" | python3 -m json.tool 2>/dev/null

## minikube-silence: create a silence for a rule (RULE=name DURATION=2h)
RULE     ?= "High memory usage"
DURATION ?= 2h
minikube-silence:
	@API_URL=$$(minikube service gosentinel-api -n $(NAMESPACE) --url 2>/dev/null | head -1); \
	echo "Creating silence for rule '$(RULE)' for $(DURATION)..."; \
	curl -s -X POST "$$API_URL/api/v1/silences" \
		-H "Content-Type: application/json" \
		-d '{"rule_name":"$(RULE)","created_by":"make","comment":"Created via make minikube-silence","duration":"$(DURATION)"}' \
		| python3 -m json.tool 2>/dev/null

## minikube-routing: update alert routing (RULE=name CHANNELS=slack,gmail)
CHANNELS ?= slack,gmail
minikube-routing:
	@API_URL=$$(minikube service gosentinel-api -n $(NAMESPACE) --url 2>/dev/null | head -1); \
	CHANNELS_JSON=$$(echo "$(CHANNELS)" | python3 -c "import sys,json; print(json.dumps(sys.stdin.read().strip().split(',')))"); \
	echo "Updating routing for rule '$(RULE)' → channels: $(CHANNELS)"; \
	curl -s -X POST "$$API_URL/api/v1/routing" \
		-H "Content-Type: application/json" \
		-d "{\"rule_name\":\"$(RULE)\",\"channels\":$$CHANNELS_JSON}" \
		| python3 -m json.tool 2>/dev/null

# ── Terraform ─────────────────────────────────────────────────────────────────

## tf-init: initialise Terraform for the given environment (TF_ENV=dev|prod)
tf-init:
	terraform -chdir=terraform/environments/$(TF_ENV) init -upgrade

## tf-plan: plan Terraform changes
tf-plan:
	terraform -chdir=terraform/environments/$(TF_ENV) plan -out=tfplan

## tf-apply: apply Terraform changes
tf-apply:
	terraform -chdir=terraform/environments/$(TF_ENV) apply tfplan

## tf-destroy: destroy all Terraform-managed resources (DANGEROUS)
tf-destroy:
	terraform -chdir=terraform/environments/$(TF_ENV) destroy

## tf-fmt: format all Terraform files
tf-fmt:
	terraform fmt -recursive terraform/

# ── Database ──────────────────────────────────────────────────────────────────

## db-migrate: run database migrations (requires GOSENTINEL_POSTGRES_DSN)
db-migrate:
	@echo "Running database migrations..."
	psql "$$GOSENTINEL_POSTGRES_DSN" -f deploy/sql/migrations.sql

## db-migrate-minikube: run migrations against Minikube PostgreSQL
db-migrate-minikube:
	@echo "Running migrations against Minikube PostgreSQL..."
	@kubectl exec -n $(NAMESPACE) deployment/postgres -- \
		psql -U gosentinel -d gosentinel -c "\
			CREATE TABLE IF NOT EXISTS alert_states ( \
				rule_name  TEXT PRIMARY KEY, \
				state      TEXT NOT NULL CHECK (state IN ('pending', 'firing')), \
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW() \
			);"
	@echo "Migrations complete."

# ── Utilities ─────────────────────────────────────────────────────────────────

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html

## help: show this help message
help:
	@echo ""
	@echo "GoSentinel Makefile — available targets:"
	@echo ""
	@grep -E '^## ' Makefile | sed 's/## /  /' | column -t -s ':'
	@echo ""
	@echo "Variables (override with make VAR=value):"
	@echo "  IMAGE_TAG        $(IMAGE_TAG)"
	@echo "  IMAGE_REPO       $(IMAGE_REPO)"
	@echo "  NAMESPACE        $(NAMESPACE)"
	@echo "  AWS_REGION       $(AWS_REGION)"
	@echo "  CLUSTER          $(CLUSTER)"
	@echo "  TF_ENV           $(TF_ENV)"
	@echo "  MINIKUBE_CPUS    $(MINIKUBE_CPUS)"
	@echo "  MINIKUBE_MEMORY  $(MINIKUBE_MEMORY)MB"
	@echo "  MINIKUBE_DRIVER  $(MINIKUBE_DRIVER)"
	@echo "  CHANNEL          $(CHANNEL)  (for minikube-test-alert)"
	@echo "  RULE             $(RULE)  (for minikube-silence, minikube-routing)"
	@echo "  DURATION         $(DURATION)  (for minikube-silence)"
	@echo "  CHANNELS         $(CHANNELS)  (for minikube-routing)"
	@echo ""
