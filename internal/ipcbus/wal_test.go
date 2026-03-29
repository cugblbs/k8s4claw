package ipcbus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func newTestMessage(channel string) *Message {
	return NewMessage(TypeMessage, channel, json.RawMessage(`{"key":"value"}`))
}

func TestWAL_AppendAndPending(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	msg := newTestMessage("ch1")
	if err := w.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	pending := w.PendingEntries()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].ID != msg.ID {
		t.Errorf("expected ID %s, got %s", msg.ID, pending[0].ID)
	}
	if pending[0].Channel != "ch1" {
		t.Errorf("expected channel ch1, got %s", pending[0].Channel)
	}
	if pending[0].State != WALPending {
		t.Errorf("expected state pending, got %s", pending[0].State)
	}
	if pending[0].Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", pending[0].Attempts)
	}
}

func TestWAL_Complete(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	msg := newTestMessage("ch1")
	if err := w.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Complete(msg.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	pending := w.PendingEntries()
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending after complete, got %d", len(pending))
	}
}

func TestWAL_Recovery(t *testing.T) {
	dir := t.TempDir()

	// Write 2 messages, complete 1, then close.
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}

	msg1 := newTestMessage("ch1")
	msg2 := newTestMessage("ch2")
	if err := w.Append(msg1); err != nil {
		t.Fatalf("Append msg1: %v", err)
	}
	if err := w.Append(msg2); err != nil {
		t.Fatalf("Append msg2: %v", err)
	}
	if err := w.Complete(msg1.ID); err != nil {
		t.Fatalf("Complete msg1: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify recovery.
	w2, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL (recovery): %v", err)
	}
	defer func() { _ = w2.Close() }()

	pending := w2.PendingEntries()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after recovery, got %d", len(pending))
	}
	if pending[0].ID != msg2.ID {
		t.Errorf("expected recovered ID %s, got %s", msg2.ID, pending[0].ID)
	}
}

func TestWAL_Compact(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}

	msg1 := newTestMessage("ch1")
	msg2 := newTestMessage("ch2")
	msg3 := newTestMessage("ch3")
	for _, msg := range []*Message{msg1, msg2, msg3} {
		if err := w.Append(msg); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Complete(msg1.ID); err != nil {
		t.Fatalf("Complete msg1: %v", err)
	}
	if err := w.Complete(msg2.ID); err != nil {
		t.Fatalf("Complete msg2: %v", err)
	}

	// Flush before measuring so all bytes are on disk.
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Measure file size before compaction.
	infoBefore, err := os.Stat(filepath.Join(dir, walFileName))
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}

	if err := w.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Verify only 1 pending remains.
	pending := w.PendingEntries()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after compact, got %d", len(pending))
	}
	if pending[0].ID != msg3.ID {
		t.Errorf("expected ID %s, got %s", msg3.ID, pending[0].ID)
	}

	// Verify file shrank.
	infoAfter, err := os.Stat(filepath.Join(dir, walFileName))
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if infoAfter.Size() >= infoBefore.Size() {
		t.Errorf("expected smaller file after compaction: before=%d after=%d",
			infoBefore.Size(), infoAfter.Size())
	}

	_ = w.Close()
}

func TestWAL_NeedsCompaction_BelowThreshold(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	// A fresh WAL with a few entries should not need compaction.
	msg := newTestMessage("ch1")
	if err := w.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if w.NeedsCompaction() {
		t.Error("expected NeedsCompaction=false for small WAL")
	}
}

func TestWAL_NeedsCompaction_AboveThreshold(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Write enough data to exceed the 10 MB threshold.
	// Build a valid JSON string payload of ~10KB.
	filler := make([]byte, 10*1024)
	for i := range filler {
		filler[i] = 'a'
	}
	bigPayload := json.RawMessage(fmt.Sprintf(`"%s"`, string(filler)))

	for i := 0; i < 1100; i++ {
		msg := NewMessage(TypeMessage, "ch-large", bigPayload)
		if err := w.Append(msg); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if !w.NeedsCompaction() {
		// Check actual file size for debugging.
		info, _ := os.Stat(w.path())
		t.Errorf("expected NeedsCompaction=true after writing >10MB, fileSize=%d", info.Size())
	}
}

func TestWAL_MarkDLQ(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	msg := newTestMessage("ch1")
	if err := w.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := w.MarkDLQ(msg.ID); err != nil {
		t.Fatalf("MarkDLQ: %v", err)
	}

	pending := w.PendingEntries()
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending after MarkDLQ, got %d", len(pending))
	}
}

func TestWAL_MarkDLQ_NotFound(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	err = w.MarkDLQ("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent entry")
	}
}

func TestWAL_Complete_NotFound(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	err = w.Complete("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent entry")
	}
}

func TestWAL_IncrementAttempts_NotFound(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	_, err = w.IncrementAttempts("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent entry")
	}
}

func TestWAL_Recovery_WithDLQ(t *testing.T) {
	dir := t.TempDir()

	// Write 2 messages, mark one as DLQ, then close.
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}

	msg1 := newTestMessage("ch1")
	msg2 := newTestMessage("ch2")
	if err := w.Append(msg1); err != nil {
		t.Fatalf("Append msg1: %v", err)
	}
	if err := w.Append(msg2); err != nil {
		t.Fatalf("Append msg2: %v", err)
	}
	if err := w.MarkDLQ(msg1.ID); err != nil {
		t.Fatalf("MarkDLQ msg1: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify only msg2 is pending.
	w2, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL (recovery): %v", err)
	}
	defer func() { _ = w2.Close() }()

	pending := w2.PendingEntries()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after recovery, got %d", len(pending))
	}
	if pending[0].ID != msg2.ID {
		t.Errorf("expected recovered ID %s, got %s", msg2.ID, pending[0].ID)
	}
}

func TestWAL_IncrementAttempts(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	msg := newTestMessage("ch1")
	if err := w.Append(msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Initial attempt count is 1; increment twice to reach 3.
	count, err := w.IncrementAttempts(msg.ID)
	if err != nil {
		t.Fatalf("IncrementAttempts (1): %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 after first increment, got %d", count)
	}

	count, err = w.IncrementAttempts(msg.ID)
	if err != nil {
		t.Fatalf("IncrementAttempts (2): %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 after second increment, got %d", count)
	}

	// Verify the in-memory entry reflects the count.
	pending := w.PendingEntries()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].Attempts != 3 {
		t.Errorf("expected 3 attempts in pending entry, got %d", pending[0].Attempts)
	}
}
