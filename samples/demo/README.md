# ConfigMap Sync Demo

This demo showcases the difference between standard
Kubernetes ConfigMap sync behavior and the instant sync provided by kube-resource-sync.

## Demo Architecture

### Standard Deployment (`deployment.yaml`)
- Uses regular ConfigMap volume mount
- Subject to kubelet sync delay (~60 seconds by default)
- Shows **delayed updates** when ConfigMap changes

### Sidecar Deployment (`deployment-with-sync.yaml`)  
- Includes **kube-resource-sync sidecar** container
- Uses emptyDir volume with sidecar-managed sync
- Shows **instant updates** when ConfigMap changes

## Quick Start

1. **Deploy the demo:**
   ```bash
   kubectl apply -f .
   ```

2. **Port-forward both services:**
   ```bash
   # Standard version (with delay)
   kubectl port-forward svc/file-watcher-service 8080:7070
   
   # Sidecar version (instant sync)  
   kubectl port-forward svc/file-watcher-with-sync-service 8090:7070
   ```

3. **Open both UIs in browser:**
   - Standard: http://localhost:8080
   - With Sidecar: http://localhost:8090

4. **Start the patching demo:**
   ```bash
   ./patch-configmap.sh
   ```

## What You'll See

- **Standard UI (port 8080)**: Updates with random delay
- **Sidecar UI (port 8090)**: Updates instantly (< 1 second)
