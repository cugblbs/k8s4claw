package ipcbus

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	messagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_ipcbus_messages_total",
		Help: "Total messages routed by the IPC Bus.",
	}, []string{"channel", "direction"}) // direction: "inbound" or "outbound"

	messagesInflight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_messages_inflight",
		Help: "Currently unACKed messages.",
	})

	bufferUsageRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claw_ipcbus_buffer_usage_ratio",
		Help: "Ring buffer fill ratio per channel.",
	}, []string{"channel"})

	spillTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "claw_ipcbus_spill_total",
		Help: "Messages spilled to disk due to full buffer.",
	})

	dlqTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "claw_ipcbus_dlq_total",
		Help: "Messages sent to the dead letter queue.",
	})

	dlqSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_dlq_size",
		Help: "Current number of entries in the DLQ.",
	})

	retryTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "claw_ipcbus_retry_total",
		Help: "Delivery retry attempts.",
	})

	bridgeConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_bridge_connected",
		Help: "RuntimeBridge connection status (0/1).",
	})

	sidecarConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_sidecar_connections",
		Help: "Number of connected sidecar clients.",
	})

	walEntries = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_wal_entries",
		Help: "Number of pending WAL entries.",
	})
)

// RecordInbound increments the inbound message counter for the given channel.
func RecordInbound(channel string) {
	messagesTotal.WithLabelValues(channel, "inbound").Inc()
	messagesInflight.Inc()
}

// RecordOutbound increments the outbound message counter for the given channel.
func RecordOutbound(channel string) {
	messagesTotal.WithLabelValues(channel, "outbound").Inc()
	messagesInflight.Dec()
}

// RecordSpill increments the spill counter.
func RecordSpill() {
	spillTotal.Inc()
}

// RecordDLQ increments the DLQ counter and sets the current DLQ size.
func RecordDLQ(currentSize int) {
	dlqTotal.Inc()
	dlqSize.Set(float64(currentSize))
}

// RecordRetry increments the retry counter.
func RecordRetry() {
	retryTotal.Inc()
}

// SetBridgeConnected sets the bridge connection status gauge.
func SetBridgeConnected(connected bool) {
	if connected {
		bridgeConnected.Set(1)
	} else {
		bridgeConnected.Set(0)
	}
}

// SetSidecarConnections sets the number of connected sidecar clients.
func SetSidecarConnections(n int) {
	sidecarConnections.Set(float64(n))
}

// SetWALEntries sets the number of pending WAL entries.
func SetWALEntries(n int) {
	walEntries.Set(float64(n))
}

// SetBufferUsage sets the ring buffer fill ratio for a given channel.
func SetBufferUsage(channel string, ratio float64) {
	bufferUsageRatio.WithLabelValues(channel).Set(ratio)
}
