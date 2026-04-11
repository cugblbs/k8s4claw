package main

import (
	"fmt"
	"strings"
)

// mockResponses maps keywords to canned responses for demo mode.
var mockResponses = map[string]string{
	"hello": "Hello! I'm an OpenClaw agent running on Kubernetes, managed by k8s4claw. How can I help your team today?",
	"help":  "I can help with:\n- Code reviews and explanations\n- Documentation drafting\n- Data analysis\n- Team knowledge Q&A\n\nJust send me a message!",
	"code":  "Here's a quick example of a Go HTTP server:\n\n```go\nhttp.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {\n    fmt.Fprintln(w, \"Hello from k8s4claw!\")\n})\nhttp.ListenAndServe(\":8080\", nil)\n```",
	"k8s":   "k8s4claw manages AI agent runtimes on Kubernetes. It handles:\n- StatefulSet lifecycle\n- IPC Bus message routing (WAL + DLQ)\n- Channel sidecars (Slack, Discord, Webhook)\n- Auto-updates with health checks and circuit breaker\n- PVC persistence and CSI snapshots",
	"status": "All systems operational:\n- Runtime: OpenClaw v0.1.0\n- IPC Bus: Connected\n- Channels: 1 active (slack-team)\n- Uptime: 2h 15m\n- Messages processed: 42",
}

const defaultMockResponse = "I received your message. In production mode (with ANTHROPIC_API_KEY set), I would process this with Claude. This is a demo response from mock mode."

func newMockHandler(model, systemPrompt string) *handler {
	return &handler{
		model:        model,
		systemPrompt: systemPrompt,
		mockMode:     true,
	}
}

func mockResponse(text string) string {
	lower := strings.ToLower(text)
	for keyword, response := range mockResponses {
		if strings.Contains(lower, keyword) {
			return response
		}
	}
	return fmt.Sprintf("%s\n\nYou said: %q", defaultMockResponse, text)
}
