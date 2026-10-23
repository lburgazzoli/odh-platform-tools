.DEFAULT_GOAL := help

## Tool Versions
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION   ?= v1.9.0
CONTROLLER_GEN_VERSION ?= v0.23.0

## Tool Commands
GOLANGCI_LINT = go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   = go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
CONTROLLER_GEN = go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

##@ General

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9\/]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2} /^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5)}' $(MAKEFILE_LIST)

##@ Dependencies

.PHONY: generate
generate: ## Generate deepcopy code with controller-gen
	$(CONTROLLER_GEN) object:headerFile=./hack/boilerplate.go.txt paths=./pkg/module/api/...

.PHONY: manifests
manifests: ## Generate the module definition CRD with controller-gen
	$(CONTROLLER_GEN) crd paths=./pkg/module/api/... output:crd:artifacts:config=config/crd/bases

.PHONY: deps
deps: ## Tidy go.mod and go.sum
	go mod tidy

##@ Build

.PHONY: build
build: ## Build all packages
	go build ./...

##@ Formatting

.PHONY: fmt
fmt: ## Format source code
	$(GOLANGCI_LINT) fmt

.PHONY: fmt-check
fmt-check: ## Check formatting without changing files
	$(GOLANGCI_LINT) fmt --diff

##@ Testing

.PHONY: test
test: ## Run unit tests
	go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Run the offline integration suite (build tag: integration)
	go test -race -count=1 -tags integration ./...

.PHONY: coverage
coverage: ## Run unit tests with coverage
	go test -race -count=1 -coverprofile=cover.out ./...
	go tool cover -func=cover.out

##@ Linting

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run linter
	$(GOLANGCI_LINT) run ./...

.PHONY: lint/fix
lint/fix: ## Run linter with auto-fix
	$(GOLANGCI_LINT) run --fix ./...

##@ Security

.PHONY: vulncheck
vulncheck: ## Run vulnerability check
	$(GOVULNCHECK) ./...

##@ Verification

.PHONY: verify
verify: fmt-check vet lint test ## Run all offline quality checks
