package channel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

const (
	maxMessageSize  = 16 * 1024 * 1024 // 16 MiB
	frameHeaderSize = 4
)

type messageType string

const (
	typeMessage   messageType = "message"
	typeAck       messageType = "ack"
	typeNack      messageType = "nack"
	typeSlowDown  messageType = "slow_down"
	typeResume    messageType = "resume"
	typeShutdown  messageType = "shutdown"
	typeRegister  messageType = "register"
	typeHeartbeat messageType = "heartbeat"
)

type message struct {
	ID            string          `json:"id"`
	Type          messageType     `json:"type"`
	Channel       string          `json:"channel,omitempty"`
	CorrelationID string          `json:"correlationId,omitempty"`
	ReplyTo       string          `json:"replyTo,omitempty"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

func newMessage(mt messageType, channel string, payload json.RawMessage) *message {
	return &message{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Type:      mt,
		Channel:   channel,
		Timestamp: time.Now(),
		Payload:   payload,
	}
}

func newAck(id string) *message {
	ref, _ := json.Marshal(map[string]string{"ref": id})
	return &message{
		ID:            uuid.Must(uuid.NewV7()).String(),
		Type:          typeAck,
		CorrelationID: id,
		Timestamp:     time.Now(),
		Payload:       json.RawMessage(ref),
	}
}

func (m *message) isControl() bool {
	switch m.Type {
	case typeAck, typeNack, typeSlowDown, typeResume, typeShutdown, typeRegister, typeHeartbeat:
		return true
	}
	return false
}

func writeMessage(w io.Writer, msg *message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	if len(data) > maxMessageSize {
		return fmt.Errorf("message size %d exceeds maximum %d", len(data), maxMessageSize)
	}
	frame := make([]byte, frameHeaderSize+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data))) //nolint:gosec // safe: len(data) bounded by maxMessageSize (16 MiB)
	copy(frame[frameHeaderSize:], data)
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("failed to write frame: %w", err)
	}
	return nil
}

func readMessage(r io.Reader) (*message, error) {
	header := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxMessageSize {
		return nil, fmt.Errorf("frame size %d exceeds maximum %d", length, maxMessageSize)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("failed to read frame body: %w", err)
	}
	var msg message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return &msg, nil
}
