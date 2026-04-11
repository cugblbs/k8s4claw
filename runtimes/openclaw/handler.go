package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/coder/websocket"
)

// message mirrors the IPC bus Message type.
type message struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Channel       string          `json:"channel,omitempty"`
	CorrelationID string          `json:"correlationId,omitempty"`
	ReplyTo       string          `json:"replyTo,omitempty"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// chatPayload is the expected payload format from channel sidecars.
type chatPayload struct {
	Text   string `json:"text"`
	User   string `json:"user,omitempty"`
	Thread string `json:"thread,omitempty"`
}

type handler struct {
	client       anthropic.Client
	model        string
	systemPrompt string
	mockMode     bool
}

func newHandler(apiKey, model, systemPrompt string) *handler {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &handler{
		client:       client,
		model:        model,
		systemPrompt: systemPrompt,
	}
}

func (h *handler) serve(ctx context.Context, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "shutdown")

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			slog.Info("websocket read ended", "error", err)
			return
		}

		var msg message
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("malformed message", "error", err)
			continue
		}

		// Skip control messages.
		if msg.Type != "message" {
			continue
		}

		resp, err := h.handleMessage(ctx, &msg)
		if err != nil {
			slog.Error("handle message failed", "error", err, "msgId", msg.ID)
			resp = h.errorResponse(&msg, err)
		}

		respData, _ := json.Marshal(resp)
		if err := conn.Write(ctx, websocket.MessageText, respData); err != nil {
			slog.Error("websocket write failed", "error", err)
			return
		}
	}
}

func (h *handler) handleMessage(ctx context.Context, msg *message) (*message, error) {
	var payload chatPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	if payload.Text == "" {
		return nil, fmt.Errorf("empty text in payload")
	}

	slog.Info("processing message", "msgId", msg.ID, "user", payload.User, "text_len", len(payload.Text))

	if h.mockMode {
		return h.buildResponse(msg, mockResponse(payload.Text)), nil
	}

	resp, err := h.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     h.model,
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: h.systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(payload.Text)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude API call failed: %w", err)
	}

	// Extract text from response.
	var responseText string
	for _, block := range resp.Content {
		if block.Type == "text" {
			responseText += block.Text
		}
	}

	slog.Info("response generated", "msgId", msg.ID, "response_len", len(responseText))

	return h.buildResponse(msg, responseText), nil
}

func (h *handler) buildResponse(original *message, text string) *message {
	payload, _ := json.Marshal(chatPayload{Text: text})
	return &message{
		ID:            fmt.Sprintf("%s-resp", original.ID),
		Type:          "message",
		Channel:       original.Channel,
		CorrelationID: original.ID,
		ReplyTo:       original.Channel,
		Timestamp:     time.Now(),
		Payload:       payload,
	}
}

func (h *handler) errorResponse(original *message, err error) *message {
	payload, _ := json.Marshal(chatPayload{
		Text: fmt.Sprintf("Sorry, I encountered an error: %v", err),
	})
	return &message{
		ID:            fmt.Sprintf("%s-err", original.ID),
		Type:          "message",
		Channel:       original.Channel,
		CorrelationID: original.ID,
		ReplyTo:       original.Channel,
		Timestamp:     time.Now(),
		Payload:       payload,
	}
}
