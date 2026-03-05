package ipcbus

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordInbound(t *testing.T) {
	RecordInbound("slack")
	RecordInbound("slack")
	RecordInbound("webhook")
	count := testutil.ToFloat64(messagesTotal.WithLabelValues("slack", "inbound"))
	if count < 2 { // use >= because promauto persists across tests
		t.Errorf("expected >= 2, got %f", count)
	}
}

func TestRecordOutbound(t *testing.T) {
	RecordOutbound("slack")
	count := testutil.ToFloat64(messagesTotal.WithLabelValues("slack", "outbound"))
	if count < 1 {
		t.Errorf("expected >= 1, got %f", count)
	}
}

func TestRecordSpill(t *testing.T) {
	RecordSpill()
	count := testutil.ToFloat64(spillTotal)
	if count < 1 {
		t.Errorf("expected >= 1, got %f", count)
	}
}

func TestRecordDLQ(t *testing.T) {
	RecordDLQ(5)
	count := testutil.ToFloat64(dlqTotal)
	if count < 1 {
		t.Errorf("expected >= 1, got %f", count)
	}
	size := testutil.ToFloat64(dlqSize)
	if size != 5 {
		t.Errorf("expected dlqSize 5, got %f", size)
	}
}

func TestRecordRetry(t *testing.T) {
	RecordRetry()
	count := testutil.ToFloat64(retryTotal)
	if count < 1 {
		t.Errorf("expected >= 1, got %f", count)
	}
}

func TestSetBridgeConnected(t *testing.T) {
	SetBridgeConnected(true)
	if v := testutil.ToFloat64(bridgeConnected); v != 1 {
		t.Errorf("expected 1, got %f", v)
	}
	SetBridgeConnected(false)
	if v := testutil.ToFloat64(bridgeConnected); v != 0 {
		t.Errorf("expected 0, got %f", v)
	}
}

func TestSetSidecarConnections(t *testing.T) {
	SetSidecarConnections(3)
	if v := testutil.ToFloat64(sidecarConnections); v != 3 {
		t.Errorf("expected 3, got %f", v)
	}
}

func TestSetWALEntries(t *testing.T) {
	SetWALEntries(42)
	if v := testutil.ToFloat64(walEntries); v != 42 {
		t.Errorf("expected 42, got %f", v)
	}
}

func TestSetBufferUsage(t *testing.T) {
	SetBufferUsage("slack", 0.75)
	if v := testutil.ToFloat64(bufferUsageRatio.WithLabelValues("slack")); v != 0.75 {
		t.Errorf("expected 0.75, got %f", v)
	}
}
