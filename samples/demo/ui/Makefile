# File Watcher Makefile

# Variables
IMAGE_NAME ?= kube-resource-sync-demo-ui
IMAGE_TAG ?= latest
REGISTRY ?= quay.io/philipgough
FULL_IMAGE_NAME = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

.PHONY: help build push

help: ## Show this help message
	@echo "File Watcher - Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build Docker image
	@echo "🔨 Building Docker image: $(FULL_IMAGE_NAME)"
	docker build -t $(FULL_IMAGE_NAME) .
	@echo "✅ Build complete!"

push: build ## Build and push Docker image to registry
	@echo "📤 Pushing image: $(FULL_IMAGE_NAME)"
	docker push $(FULL_IMAGE_NAME)
	@echo "✅ Push complete!"
