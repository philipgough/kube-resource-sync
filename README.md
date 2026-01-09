# Kube Resource Sync

A Kubernetes controller designed to run as a sidecar container that watches 
ConfigMaps and Secrets for changes and immediately synchronizes them to the 
local filesystem, bypassing the default kubelet sync delay.

## Overview

When using ConfigMaps and Secrets mounted as volumes in Kubernetes, the kubelet 
updates these files periodically (typically every 60 seconds). This delay can 
be problematic for applications that need immediate access to updated 
configuration or secrets.

Kube Resource Sync solves this by:
- Watching specific ConfigMaps and Secrets for changes using the Kubernetes API
- Immediately writing updates to designated file paths
- Running as a lightweight sidecar container alongside your main application

## Features

- Real-time synchronization of ConfigMap and Secret changes
- Configurable watch patterns for specific resources
- Minimal resource footprint suitable for sidecar deployment
- Support for multiple file formats and directory structures
- Health checks and monitoring endpoints
- Init container mode for one-time synchronization

## Demo

See the [demo example](./samples/demo/) for a complete working example that shows the difference
between standard Kubernetes ConfigMap mounting (periodic kubelet sync) versus instant sidecar sync.

![Demo](./samples/demo/demo.gif)

## Init Container Mode

Kube Resource Sync can also be used as an init container to perform a one-time synchronization before your main application starts. In this mode, the application will:

1. Wait for the specified ConfigMap or Secret to be available
2. Write the resource data to the specified file path
3. Exit successfully once the sync is complete

This is useful when you need to ensure that configuration files are available before your main application starts.

### Usage

To run in init container mode, add the `--init-mode` flag:

```bash
./kube-resource-sync \
  --init-mode \
  --namespace=default \
  --resource-type=configmap \
  --resource-name=my-config \
  --resource-key=config.yaml \
  --write-path=/etc/config/config.yaml
```

### Configuration

The init mode supports the same configuration flags as the normal mode, with these differences:

- `--init-mode`: Enable init container mode
- `--listen`: Ignored in init mode (no HTTP server is started)
