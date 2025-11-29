# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
BINARY_NAME=kube-resource-sync
MAIN_PACKAGE=./cmd/main.go

# Docker parameters
DOCKER_REGISTRY=quay.io/philipgough
IMAGE_NAME=$(DOCKER_REGISTRY)/$(BINARY_NAME)
IMAGE_TAG?=latest

# Build targets
.PHONY: build clean test lint docker-build docker-push help
build: ## Build the binary
	$(GOBUILD) -o $(BINARY_NAME) -v $(MAIN_PACKAGE)

test: ## Run tests
	$(GOTEST) -v ./...

clean: ## Clean build artifacts
	$(GOCLEAN)
	rm -f $(BINARY_NAME)

lint: ## Run linter (if available)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found, skipping lint"; \
	fi

docker-build: ## Build Docker image
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) .

docker-push: docker-build ## Build and push Docker image to registry
	docker push $(IMAGE_NAME):$(IMAGE_TAG)

help: ## Show help
	@echo 'Usage:'
	@echo '  make <target>'
	@echo ''
	@echo 'Targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
