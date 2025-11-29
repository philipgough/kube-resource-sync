package sync

import (
	"crypto/md5"
	"encoding/binary"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics holds the prometheus metrics for the controller
type metrics struct {
	// configMapHash tracks the hash of the currently synced ConfigMap data
	configMapHash prometheus.Gauge
	// configMapLastWriteSuccessTime tracks when the file was last successfully written
	configMapLastWriteSuccessTime prometheus.Gauge
}

// newMetrics creates and returns new controller metrics
func newMetrics() *metrics {
	return &metrics{
		configMapHash: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kube_resource_sync_configmap_hash",
			Help: "Hash of the currently synced ConfigMap data",
		}),
		configMapLastWriteSuccessTime: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kube_resource_sync_last_write_success_timestamp_seconds",
			Help: "Timestamp of the last successful file write",
		}),
	}
}

// register registers all metrics with the given registry
func (m *metrics) register(registry prometheus.Registerer) {
	registry.MustRegister(
		m.configMapHash,
		m.configMapLastWriteSuccessTime,
	)
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
