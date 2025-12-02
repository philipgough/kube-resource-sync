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

## Demo

See the [demo example](./samples/demo/) for a complete working example that shows the difference
between standard Kubernetes ConfigMap mounting (periodic kubelet sync) versus instant sidecar sync.

https://github.com/philipgough/kube-resource-sync/raw/main/samples/demo/demo.mov
