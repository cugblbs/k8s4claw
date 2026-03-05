package ipcbus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WALState represents the state of a message in the write-ahead log.
type WALState string

const (
	WALPending  WALState = "pending"
	WALComplete WALState = "complete"
	WALDLQ      WALState = "dlq"

	walFileName         = "wal.jsonl"
	compactionThreshold = 10 * 1024 * 1024 // 10 MB
)

// WALEntry is a single record in the write-ahead log.
type WALEntry struct {
	ID       string   `json:"id"`
	Channel  string   `json:"channel"`
	State    WALState `json:"state"`
	Attempts int      `json:"attempts"`
	TS       string   `json:"ts"`
	Msg      *Message `json:"msg,omitempty"`
}

// WAL is an append-only write-ahead log backed by a JSON-lines file.
// It supports recovery by replaying the log on startup and compaction
// to reclaim space from completed or dead-lettered entries.
type WAL struct {
	mu      sync.Mutex
	dir     string
	file    *os.File
	writer  *bufio.Writer
	entries map[string]*WALEntry
}

// NewWAL creates the WAL directory if needed, recovers any existing log,
// and opens the file for appending.
func NewWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory: %w", err)
	}

	w := &WAL{
		dir:     dir,
		entries: make(map[string]*WALEntry),
	}

	if err := w.recover(); err != nil {
		return nil, fmt.Errorf("failed to recover WAL: %w", err)
	}

	f, err := os.OpenFile(w.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL file: %w", err)
	}
	w.file = f
	w.writer = bufio.NewWriter(f)

	return w, nil
}

func (w *WAL) path() string {
	return filepath.Join(w.dir, walFileName)
}

// recover replays the existing WAL file, keeping only pending entries in memory.
func (w *WAL) recover() error {
	f, err := os.Open(w.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open WAL for recovery: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxMessageSize)

	for scanner.Scan() {
		var entry WALEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			// Skip corrupt lines during recovery.
			continue
		}
		switch entry.State {
		case WALPending:
			w.entries[entry.ID] = &entry
		case WALComplete, WALDLQ:
			delete(w.entries, entry.ID)
		}
	}

	return scanner.Err()
}

// appendEntry writes a single WALEntry as a JSON line.
func (w *WAL) appendEntry(entry *WALEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal WAL entry: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write WAL entry: %w", err)
	}
	return nil
}

// Append writes a new pending entry for the given message.
func (w *WAL) Append(msg *Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := &WALEntry{
		ID:       msg.ID,
		Channel:  msg.Channel,
		State:    WALPending,
		Attempts: 1,
		TS:       time.Now().UTC().Format(time.RFC3339Nano),
		Msg:      msg,
	}

	if err := w.appendEntry(entry); err != nil {
		return err
	}
	w.entries[msg.ID] = entry
	return nil
}

// Complete marks a message as complete in the WAL.
func (w *WAL) Complete(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry, ok := w.entries[id]
	if !ok {
		return fmt.Errorf("WAL entry %q not found", id)
	}

	record := &WALEntry{
		ID:       entry.ID,
		Channel:  entry.Channel,
		State:    WALComplete,
		Attempts: entry.Attempts,
		TS:       time.Now().UTC().Format(time.RFC3339Nano),
	}

	if err := w.appendEntry(record); err != nil {
		return err
	}
	delete(w.entries, id)
	return nil
}

// MarkDLQ marks a message as dead-letter queued in the WAL.
func (w *WAL) MarkDLQ(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry, ok := w.entries[id]
	if !ok {
		return fmt.Errorf("WAL entry %q not found", id)
	}

	record := &WALEntry{
		ID:       entry.ID,
		Channel:  entry.Channel,
		State:    WALDLQ,
		Attempts: entry.Attempts,
		TS:       time.Now().UTC().Format(time.RFC3339Nano),
	}

	if err := w.appendEntry(record); err != nil {
		return err
	}
	delete(w.entries, id)
	return nil
}

// IncrementAttempts bumps the retry count for a pending entry and returns
// the new attempt count.
func (w *WAL) IncrementAttempts(id string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry, ok := w.entries[id]
	if !ok {
		return 0, fmt.Errorf("WAL entry %q not found", id)
	}

	entry.Attempts++
	entry.TS = time.Now().UTC().Format(time.RFC3339Nano)

	if err := w.appendEntry(entry); err != nil {
		return 0, err
	}
	return entry.Attempts, nil
}

// PendingEntries returns all entries that are still in the pending state.
func (w *WAL) PendingEntries() []*WALEntry {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make([]*WALEntry, 0, len(w.entries))
	for _, entry := range w.entries {
		result = append(result, entry)
	}
	return result
}

// Compact rewrites the WAL file to contain only the current pending entries.
func (w *WAL) Compact() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	tmpPath := w.path() + ".tmp"
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create compaction temp file: %w", err)
	}

	bw := bufio.NewWriter(tmp)
	for _, entry := range w.entries {
		data, err := json.Marshal(entry)
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("failed to marshal entry during compaction: %w", err)
		}
		data = append(data, '\n')
		if _, err := bw.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("failed to write entry during compaction: %w", err)
		}
	}

	if err := bw.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to flush compaction file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to sync compaction file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close compaction file: %w", err)
	}

	// Close the current file before rename.
	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL before compaction: %w", err)
	}
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("failed to close WAL before compaction: %w", err)
	}

	if err := os.Rename(tmpPath, w.path()); err != nil {
		return fmt.Errorf("failed to rename compaction file: %w", err)
	}

	// Reopen file for appending.
	f, err := os.OpenFile(w.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to reopen WAL after compaction: %w", err)
	}
	w.file = f
	w.writer = bufio.NewWriter(f)

	return nil
}

// Flush flushes the buffered writer and fsyncs the underlying file.
func (w *WAL) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL buffer: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync WAL file: %w", err)
	}
	return nil
}

// NeedsCompaction returns true when the WAL file exceeds the compaction threshold.
func (w *WAL) NeedsCompaction() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	info, err := w.file.Stat()
	if err != nil {
		return false
	}
	return info.Size() > compactionThreshold
}

// Close flushes and closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL on close: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync WAL on close: %w", err)
	}
	return w.file.Close()
}
