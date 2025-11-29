package sync

import (
	"crypto/md5"
	"encoding/binary"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics holds the prometheus metrics for the controller
type metrics struct {
	// resourceDataHash tracks the hash of the currently synced resource data
	resourceDataHash *prometheus.GaugeVec
	// lastWriteSuccessTime tracks when the file was last successfully written
	lastWriteSuccessTime *prometheus.GaugeVec
}

// newMetrics creates and returns new controller metrics
func newMetrics() *metrics {
	return &metrics{
		resourceDataHash: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kube_resource_sync_resource_data_hash",
			Help: "Hash of the currently synced Kubernetes resource data",
		}, []string{"resource_type", "namespace", "resource_name", "key"}),
		lastWriteSuccessTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kube_resource_sync_last_write_success_timestamp_seconds",
			Help: "Timestamp of the last successful file write for a Kubernetes resource",
		}, []string{"resource_type", "namespace", "resource_name", "key"}),
	}
}

// register registers all metrics with the given registry
func (m *metrics) register(registry prometheus.Registerer) {
	registry.MustRegister(
		m.resourceDataHash,
		m.lastWriteSuccessTime,
	)
}

// setResourceDataHash sets the resource data hash metric with labels
func (m *metrics) setResourceDataHash(resourceType, namespace, resourceName, key string, hash float64) {
	m.resourceDataHash.WithLabelValues(resourceType, namespace, resourceName, key).Set(hash)
}

// setLastWriteSuccessTime sets the last write success time metric with labels
func (m *metrics) setLastWriteSuccessTime(resourceType, namespace, resourceName, key string, timestamp float64) {
	m.lastWriteSuccessTime.WithLabelValues(resourceType, namespace, resourceName, key).Set(timestamp)
}

// hashAsMetricValue converts a byte slice to a float64 hash value for metrics
func hashAsMetricValue(data []byte) float64 {
	sum := md5.Sum(data)
	// We only want 48 bits as a float64 only has a 53 bit mantissa.
	smallSum := sum[0:6]
	bytes := make([]byte, 8)
	copy(bytes, smallSum)
	return float64(binary.LittleEndian.Uint64(bytes))
}
