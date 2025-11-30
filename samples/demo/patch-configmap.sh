#!/bin/bash

# Script to continuously patch the ConfigMap to demonstrate sync behavior
# This will show the difference between standard kubelet sync and kube-resource-sync

set -e

NAMESPACE="default"
CONFIGMAP_NAME="sample-config"
KEY="config.yaml"

echo "🚀 Starting ConfigMap patching demo..."
echo "📊 This will patch '$CONFIGMAP_NAME' every 3-10 seconds to demonstrate sync differences"
echo "🔍 Watch both UIs to see instant vs delayed updates"
echo ""

# Check if ConfigMap exists
if ! kubectl get configmap "$CONFIGMAP_NAME" -n "$NAMESPACE" &>/dev/null; then
    echo "❌ ConfigMap '$CONFIGMAP_NAME' not found in namespace '$NAMESPACE'"
    echo "💡 Please deploy the demo first: kubectl apply -f ."
    exit 1
fi

echo "✅ ConfigMap found, starting patch loop..."
echo "⏹️  Press Ctrl+C to stop"
echo ""

counter=1
while true; do
    # Generate random values to show visible changes
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    random_port=$((8000 + RANDOM % 1000))
    random_timeout=$((10 + RANDOM % 90))
    log_level=$([ $((RANDOM % 2)) -eq 0 ] && echo "info" || echo "debug")
    metrics_enabled=$([ $((RANDOM % 2)) -eq 0 ] && echo "true" || echo "false")
    
    # Create updated YAML content
    new_config="server:
  host: 0.0.0.0
  port: $random_port
  timeout: ${random_timeout}s

logging:
  level: $log_level
  format: json
  
features:
  metrics: $metrics_enabled
  health_check: true

# Updated at: $timestamp
# Update #: $counter"

    # Patch the ConfigMap
    echo "🔄 Update #$counter - Port: $random_port, Timeout: ${random_timeout}s, Logs: $log_level, Metrics: $metrics_enabled"
    
    kubectl patch configmap "$CONFIGMAP_NAME" -n "$NAMESPACE" --patch "$(cat <<EOF
data:
  $KEY: |
$(echo "$new_config" | sed 's/^/    /')
EOF
)"

    if [ $? -eq 0 ]; then
        echo "✅ ConfigMap patched successfully"
    else
        echo "❌ Failed to patch ConfigMap"
    fi
    
    # Random delay between 3-10 seconds
    delay=$((3 + RANDOM % 8))
    echo "⏳ Waiting ${delay} seconds before next update..."
    echo ""
    
    sleep $delay
    counter=$((counter + 1))
done