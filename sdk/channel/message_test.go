package channel

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWriteReadMessage(t *testing.T) {
	var buf bytes.Buffer
	msg := &message{
		ID:        "test-123",
		Type:      typeMessage,
		Channel:   "slack",
		Timestamp: time.Now().Truncate(time.Millisecond),
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}

	if err := writeMessage(&buf, msg); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	got, err := readMessage(&buf)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}

	if got.ID != msg.ID {
		t.Errorf("ID = %q, want %q", got.ID, msg.ID)
	}
	if got.Type != typeMessage {
		t.Errorf("Type = %q, want %q", got.Type, typeMessage)
	}
	if got.Channel != "slack" {
		t.Errorf("Channel = %q, want %q", got.Channel, "slack")
	}
}

func TestWriteMessage_ExceedsMaxSize(t *testing.T) {
	var buf bytes.Buffer
	huge := make(json.RawMessage, maxMessageSize+1)
	msg := &message{ID: "big", Type: typeMessage, Payload: huge}

	err := writeMessage(&buf, msg)
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
}

func TestMessageTypes(t *testing.T) {
	tests := []struct {
		mt      messageType
		control bool
	}{
		{typeMessage, false},
		{typeAck, true},
		{typeNack, true},
		{typeSlowDown, true},
		{typeResume, true},
		{typeShutdown, true},
		{typeRegister, true},
		{typeHeartbeat, true},
	}
	for _, tt := range tests {
		msg := &message{Type: tt.mt}
		if msg.isControl() != tt.control {
			t.Errorf("%s.isControl() = %v, want %v", tt.mt, msg.isControl(), tt.control)
		}
	}
}
