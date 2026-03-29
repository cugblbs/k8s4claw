package ipcbus

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketMessages = []byte("messages")
	bucketIndex    = []byte("index")
)

// DLQEntry represents a single dead-lettered message stored in the DLQ.
type DLQEntry struct {
	ID        string   `json:"id"`
	Channel   string   `json:"channel"`
	Attempts  int      `json:"attempts"`
	CreatedAt string   `json:"createdAt"`
	Msg       *Message `json:"msg"`
}

// DLQ is a BoltDB-backed dead letter queue for messages that have
// exhausted their delivery attempts.
type DLQ struct {
	db      *bolt.DB
	maxSize int
	ttl     time.Duration
}

// NewDLQ opens (or creates) a BoltDB file at path and returns a DLQ
// configured with the given capacity and TTL.
func NewDLQ(path string, maxSize int, ttl time.Duration) (*DLQ, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open DLQ database: %w", err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketMessages); err != nil {
			return fmt.Errorf("failed to create messages bucket: %w", err)
		}
		if _, err := tx.CreateBucketIfNotExists(bucketIndex); err != nil {
			return fmt.Errorf("failed to create index bucket: %w", err)
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &DLQ{
		db:      db,
		maxSize: maxSize,
		ttl:     ttl,
	}, nil
}

// Put stores a message in the DLQ. If the DLQ is at capacity, the oldest
// entry is evicted first.
func (d *DLQ) Put(msg *Message, attempts int) error {
	entry := &DLQEntry{
		ID:        msg.ID,
		Channel:   msg.Channel,
		Attempts:  attempts,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Msg:       msg,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal DLQ entry: %w", err)
	}

	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)

		// Evict oldest if at capacity.
		if b.Stats().KeyN >= d.maxSize {
			if err := d.evictOldest(b); err != nil {
				return fmt.Errorf("failed to evict oldest entry: %w", err)
			}
		}

		if err := b.Put([]byte(entry.ID), data); err != nil {
			return fmt.Errorf("failed to put DLQ entry: %w", err)
		}
		return nil
	})
}

// Get retrieves a DLQ entry by message ID. Returns nil if not found.
func (d *DLQ) Get(id string) (*DLQEntry, error) {
	var entry *DLQEntry

	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		data := b.Get([]byte(id))
		if data == nil {
			return nil
		}

		entry = &DLQEntry{}
		if err := json.Unmarshal(data, entry); err != nil {
			return fmt.Errorf("failed to unmarshal DLQ entry: %w", err)
		}
		return nil
	})

	return entry, err
}

// Delete removes a DLQ entry by message ID.
func (d *DLQ) Delete(id string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		return b.Delete([]byte(id))
	})
}

// List returns all entries in the DLQ, ordered by creation time.
func (d *DLQ) List() ([]*DLQEntry, error) {
	var entries []*DLQEntry

	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		return b.ForEach(func(k, v []byte) error {
			var e DLQEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("failed to unmarshal DLQ entry: %w", err)
			}
			entries = append(entries, &e)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt < entries[j].CreatedAt
	})

	return entries, nil
}

// Size returns the number of entries in the DLQ.
func (d *DLQ) Size() int {
	var n int
	_ = d.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucketMessages).Stats().KeyN
		return nil
	})
	return n
}

// PurgeExpired removes entries older than the configured TTL and returns
// the number of entries purged.
func (d *DLQ) PurgeExpired() (int, error) {
	cutoff := time.Now().UTC().Add(-d.ttl)
	purged := 0

	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		var toDelete [][]byte

		if err := b.ForEach(func(k, v []byte) error {
			var e DLQEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("failed to unmarshal DLQ entry: %w", err)
			}

			t, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
			if err != nil {
				return fmt.Errorf("failed to parse CreatedAt: %w", err)
			}

			if t.Before(cutoff) {
				toDelete = append(toDelete, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}

		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return fmt.Errorf("failed to delete expired entry: %w", err)
			}
			purged++
		}
		return nil
	})

	return purged, err
}

// Close closes the underlying BoltDB database.
func (d *DLQ) Close() error {
	return d.db.Close()
}

// evictOldest removes the entry with the earliest CreatedAt timestamp.
func (d *DLQ) evictOldest(b *bolt.Bucket) error {
	var oldestKey []byte
	var oldestTime string

	if err := b.ForEach(func(k, v []byte) error {
		var e DLQEntry
		if err := json.Unmarshal(v, &e); err != nil {
			return fmt.Errorf("failed to unmarshal DLQ entry: %w", err)
		}
		if oldestKey == nil || e.CreatedAt < oldestTime {
			oldestKey = append([]byte(nil), k...)
			oldestTime = e.CreatedAt
		}
		return nil
	}); err != nil {
		return err
	}

	if oldestKey != nil {
		return b.Delete(oldestKey)
	}
	return nil
}
