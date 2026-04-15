BINARIES    := collector pipeline api ui
BUILD_DIR   := bin
MODULE      := github.com/yourorg/gosentinel
IMAGE_REPO  ?= gosentinel
IMAGE_TAG   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
AWS_REGION  ?= us-east-1
CLUSTER     ?= gosentinel-prod
TF_ENV      ?= dev

.PHONY: build test lint proto-gen docker-up docker-down clean \
        docker-build docker-push k8s-apply k8s-delete \
        tf-init tf-plan tf-apply tf-destroy \
        kubeconfig db-migrate coverage

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
		GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -ldflags="-X main.version=$(IMAGE_TAG)" \
		         -o $(BUILD_DIR)/$$bin-linux ./cmd/$$bin/; \
	done

# ── Test ──────────────────────────────────────────────────────────────────────

## test: run all tests with race detector
test:
	go test -race -count=1 -timeout=120s ./...

## test-short: run tests without integration tests
test-short:
	go test -race -short -count=1 ./...

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

# ── Docker ────────────────────────────────────────────────────────────────────

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
		echo "  Building gosentinel/$$svc:$(IMAGE_TAG)..."; \
		docker build -t $(IMAGE_REPO)/$$svc:$(IMAGE_TAG) \
		             -f deploy/docker/$$svc.Dockerfile .; \
	done
	@for svc in order-service inventory-service payment-service; do \
		echo "  Building gosentinel/$$svc:$(IMAGE_TAG)..."; \
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

# ── Kubernetes ────────────────────────────────────────────────────────────────

## kubeconfig: update kubeconfig for the EKS cluster
kubeconfig:
	aws eks update-kubeconfig --region $(AWS_REGION) --name $(CLUSTER)

## k8s-apply: apply all Kubernetes manifests
k8s-apply: kubeconfig
	kubectl apply -f deploy/k8s/namespace.yaml
	kubectl apply -f deploy/k8s/rbac/
	kubectl apply -f deploy/k8s/network/
	kubectl apply -f deploy/k8s/gosentinel/configmap.yaml
	kubectl apply -f deploy/k8s/backends/
	kubectl apply -f deploy/k8s/gosentinel/
	kubectl apply -f deploy/k8s/monitoring/
	@echo "Waiting for rollout..."
	kubectl rollout status deployment/gosentinel-pipeline -n gosentinel --timeout=120s
	kubectl rollout status deployment/gosentinel-api      -n gosentinel --timeout=120s
	kubectl rollout status deployment/gosentinel-ui       -n gosentinel --timeout=120s

## k8s-delete: delete all GoSentinel resources (keeps namespace)
k8s-delete: kubeconfig
	kubectl delete -f deploy/k8s/gosentinel/ --ignore-not-found
	kubectl delete -f deploy/k8s/monitoring/ --ignore-not-found

## k8s-status: show status of all GoSentinel pods
k8s-status: kubeconfig
	kubectl get pods,svc,hpa,pdb -n gosentinel

## k8s-logs: tail logs from pipeline
k8s-logs: kubeconfig
	kubectl logs -f -l app.kubernetes.io/name=gosentinel-pipeline -n gosentinel --all-containers

## helm-install: install GoSentinel via Helm
helm-install: kubeconfig
	helm upgrade --install gosentinel deploy/helm/gosentinel \
		--namespace gosentinel \
		--create-namespace \
		--set pipeline.image.tag=$(IMAGE_TAG) \
		--set api.image.tag=$(IMAGE_TAG) \
		--set ui.image.tag=$(IMAGE_TAG) \
		--wait --timeout=5m

## helm-uninstall: uninstall GoSentinel Helm release
helm-uninstall: kubeconfig
	helm uninstall gosentinel --namespace gosentinel

# ── Terraform ─────────────────────────────────────────────────────────────────

## tf-init: initialise Terraform for the given environment
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

## db-migrate: run database migrations
db-migrate:
	@echo "Running database migrations..."
	psql "$$GOSENTINEL_POSTGRES_DSN" -f deploy/sql/migrations.sql

# ── Utilities ─────────────────────────────────────────────────────────────────

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html

## help: show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //'
