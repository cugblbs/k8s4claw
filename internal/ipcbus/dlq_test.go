package ipcbus

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func newTestDLQ(t *testing.T, maxSize int, ttl time.Duration) *DLQ {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dlq.db")
	dlq, err := NewDLQ(path, maxSize, ttl)
	if err != nil {
		t.Fatalf("failed to create DLQ: %v", err)
	}
	t.Cleanup(func() { dlq.Close() })
	return dlq
}

func testMessage(channel string) *Message {
	return NewMessage(TypeMessage, channel, json.RawMessage(`{"key":"value"}`))
}

func TestDLQ_PutGet(t *testing.T) {
	dlq := newTestDLQ(t, 10000, 24*time.Hour)

	msg := testMessage("test-chan")
	if err := dlq.Put(msg, 3); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	got, err := dlq.Get(msg.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected entry, got nil")
	}
	if got.ID != msg.ID {
		t.Errorf("ID mismatch: got %s, want %s", got.ID, msg.ID)
	}
	if got.Channel != "test-chan" {
		t.Errorf("Channel mismatch: got %s, want test-chan", got.Channel)
	}
	if got.Attempts != 3 {
		t.Errorf("Attempts mismatch: got %d, want 3", got.Attempts)
	}
	if got.CreatedAt == "" {
		t.Error("CreatedAt should not be empty")
	}
	if got.Msg == nil {
		t.Error("Msg should not be nil")
	}
	if got.Msg.ID != msg.ID {
		t.Errorf("Msg.ID mismatch: got %s, want %s", got.Msg.ID, msg.ID)
	}
}

func TestDLQ_Delete(t *testing.T) {
	dlq := newTestDLQ(t, 10000, 24*time.Hour)

	msg := testMessage("test-chan")
	if err := dlq.Put(msg, 1); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := dlq.Delete(msg.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	got, err := dlq.Get(msg.ID)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got entry with ID %s", got.ID)
	}
}

func TestDLQ_List(t *testing.T) {
	dlq := newTestDLQ(t, 10000, 24*time.Hour)

	for i := range 5 {
		msg := testMessage("chan-" + string(rune('a'+i)))
		if err := dlq.Put(msg, 1); err != nil {
			t.Fatalf("Put %d failed: %v", i, err)
		}
	}

	entries, err := dlq.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(entries))
	}
}

func TestDLQ_Eviction(t *testing.T) {
	dlq := newTestDLQ(t, 3, 24*time.Hour)

	msgs := make([]*Message, 4)
	for i := range 4 {
		msgs[i] = testMessage("chan")
		if err := dlq.Put(msgs[i], 1); err != nil {
			t.Fatalf("Put %d failed: %v", i, err)
		}
		// Small sleep to ensure distinct CreatedAt timestamps for ordering.
		time.Sleep(2 * time.Millisecond)
	}

	if sz := dlq.Size(); sz != 3 {
		t.Errorf("expected size 3, got %d", sz)
	}

	// First message should have been evicted.
	got, err := dlq.Get(msgs[0].ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != nil {
		t.Error("expected first message to be evicted, but it still exists")
	}

	// Last three should still exist.
	for i := 1; i <= 3; i++ {
		got, err := dlq.Get(msgs[i].ID)
		if err != nil {
			t.Fatalf("Get msgs[%d] failed: %v", i, err)
		}
		if got == nil {
			t.Errorf("expected msgs[%d] to exist, got nil", i)
		}
	}
}

func TestDLQ_PurgeExpired(t *testing.T) {
	// Use a very short TTL so entries expire immediately.
	dlq := newTestDLQ(t, 10000, 50*time.Millisecond)

	// Create some entries that will expire.
	for i := range 3 {
		msg := testMessage("chan-" + string(rune('a'+i)))
		if err := dlq.Put(msg, 1); err != nil {
			t.Fatalf("Put %d failed: %v", i, err)
		}
	}

	// Wait for TTL to pass.
	time.Sleep(100 * time.Millisecond)

	// Add a fresh entry that should survive.
	freshMsg := testMessage("chan-fresh")
	if err := dlq.Put(freshMsg, 2); err != nil {
		t.Fatalf("Put fresh failed: %v", err)
	}

	purged, err := dlq.PurgeExpired()
	if err != nil {
		t.Fatalf("PurgeExpired failed: %v", err)
	}
	if purged != 3 {
		t.Errorf("expected 3 purged, got %d", purged)
	}

	if sz := dlq.Size(); sz != 1 {
		t.Errorf("expected size 1 after purge, got %d", sz)
	}

	// The fresh entry should still be there.
	got, err := dlq.Get(freshMsg.ID)
	if err != nil {
		t.Fatalf("Get fresh failed: %v", err)
	}
	if got == nil {
		t.Error("expected fresh entry to survive purge")
	}
}

func TestDLQ_PurgeExpired_NoneExpired(t *testing.T) {
	dlq := newTestDLQ(t, 10000, 1*time.Hour)

	msg := testMessage("chan")
	if err := dlq.Put(msg, 1); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	purged, err := dlq.PurgeExpired()
	if err != nil {
		t.Fatalf("PurgeExpired failed: %v", err)
	}
	if purged != 0 {
		t.Errorf("expected 0 purged, got %d", purged)
	}

	if sz := dlq.Size(); sz != 1 {
		t.Errorf("expected size 1, got %d", sz)
	}
}

func TestDLQ_PurgeExpired_Empty(t *testing.T) {
	dlq := newTestDLQ(t, 10000, 1*time.Millisecond)

	purged, err := dlq.PurgeExpired()
	if err != nil {
		t.Fatalf("PurgeExpired failed: %v", err)
	}
	if purged != 0 {
		t.Errorf("expected 0 purged on empty DLQ, got %d", purged)
	}
}

func TestDLQ_Size(t *testing.T) {
	dlq := newTestDLQ(t, 10000, 24*time.Hour)

	if sz := dlq.Size(); sz != 0 {
		t.Errorf("expected size 0 on empty DLQ, got %d", sz)
	}

	msg := testMessage("chan")
	if err := dlq.Put(msg, 1); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if sz := dlq.Size(); sz != 1 {
		t.Errorf("expected size 1, got %d", sz)
	}
}
