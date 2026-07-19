// Package audit provides durable storage for privileged action audit records.
package audit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	OutcomeStarted   = "started"
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeUnknown   = "unknown"

	maxListLimit = 100
)

var (
	recordsBucket = []byte("records")
	ErrNotFound   = errors.New("audit record not found")
)

// Attempt contains the fixed, non-sensitive fields recorded before an action.
type Attempt struct {
	Action   string
	Resource string
	Username string
	UID      int
}

// Record is the durable audit history for one action attempt.
type Record struct {
	ID            uint64     `json:"id"`
	Action        string     `json:"action"`
	Resource      string     `json:"resource"`
	Username      string     `json:"username"`
	UID           int        `json:"uid"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Outcome       string     `json:"outcome"`
	ErrorCategory string     `json:"error_category,omitempty"`
	DurationMS    int64      `json:"duration_ms"`
}

// Filter selects records by exact action and outcome. Limit defaults to 100.
type Filter struct {
	Limit   int
	Action  string
	Outcome string
}

// Store is a durable audit record store.
type Store struct {
	db         *bolt.DB
	maxRecords int
}

// Open opens or creates an audit store and recovers interrupted records.
func Open(path string, maxRecords int) (*Store, error) {
	if maxRecords <= 0 {
		return nil, fmt.Errorf("maxRecords must be positive")
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open audit database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure audit database: %w", err)
	}

	store := &Store{db: db, maxRecords: maxRecords}
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(recordsBucket)
		if err != nil {
			return fmt.Errorf("create records bucket: %w", err)
		}

		now := time.Now().UTC()
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var record Record
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode audit record: %w", err)
			}
			if record.Outcome != OutcomeStarted {
				continue
			}
			finishRecord(&record, now, OutcomeUnknown, "")
			encoded, err := json.Marshal(record)
			if err != nil {
				return fmt.Errorf("encode recovered audit record: %w", err)
			}
			if err := bucket.Put(key, encoded); err != nil {
				return fmt.Errorf("recover audit record: %w", err)
			}
		}

		return trimOldest(bucket, maxRecords)
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize audit database: %w", err)
	}

	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Begin durably records an action attempt before returning it.
func (s *Store) Begin(ctx context.Context, attempt Attempt) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}

	record := Record{
		Action:    attempt.Action,
		Resource:  attempt.Resource,
		Username:  attempt.Username,
		UID:       attempt.UID,
		StartedAt: time.Now().UTC(),
		Outcome:   OutcomeStarted,
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(recordsBucket)
		id, err := bucket.NextSequence()
		if err != nil {
			return fmt.Errorf("allocate audit record ID: %w", err)
		}
		record.ID = id
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode audit record: %w", err)
		}
		if err := bucket.Put(recordKey(id), encoded); err != nil {
			return fmt.Errorf("store audit record: %w", err)
		}
		return trimOldest(bucket, s.maxRecords)
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

// Complete durably records the result of an action.
func (s *Store) Complete(ctx context.Context, id uint64, outcome, errorCategory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if outcome != OutcomeSucceeded && outcome != OutcomeFailed && outcome != OutcomeUnknown {
		return fmt.Errorf("invalid final outcome %q", outcome)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(recordsBucket)
		key := recordKey(id)
		value := bucket.Get(key)
		if value == nil {
			return ErrNotFound
		}
		var record Record
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode audit record: %w", err)
		}
		finishRecord(&record, time.Now().UTC(), outcome, errorCategory)
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode audit record: %w", err)
		}
		if err := bucket.Put(key, encoded); err != nil {
			return fmt.Errorf("store completed audit record: %w", err)
		}
		return trimOldest(bucket, s.maxRecords)
	})
}

// List returns matching records newest first.
func (s *Store) List(ctx context.Context, filter Filter) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter.Limit < 0 {
		return nil, fmt.Errorf("limit must not be negative")
	}
	if filter.Outcome != "" && !validOutcome(filter.Outcome) {
		return nil, fmt.Errorf("invalid outcome filter %q", filter.Outcome)
	}
	limit := filter.Limit
	if limit == 0 || limit > maxListLimit {
		limit = maxListLimit
	}

	records := make([]Record, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(recordsBucket).Cursor()
		for _, value := cursor.Last(); value != nil && len(records) < limit; _, value = cursor.Prev() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var record Record
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode audit record: %w", err)
			}
			if filter.Action != "" && record.Action != filter.Action {
				continue
			}
			if filter.Outcome != "" && record.Outcome != filter.Outcome {
				continue
			}
			records = append(records, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func finishRecord(record *Record, finishedAt time.Time, outcome, errorCategory string) {
	record.FinishedAt = &finishedAt
	record.Outcome = outcome
	record.ErrorCategory = errorCategory
	record.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	if record.DurationMS < 0 {
		record.DurationMS = 0
	}
}

func validOutcome(outcome string) bool {
	return outcome == OutcomeStarted || outcome == OutcomeSucceeded || outcome == OutcomeFailed || outcome == OutcomeUnknown
}

func recordKey(id uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, id)
	return key
}

func trimOldest(bucket *bolt.Bucket, maxRecords int) error {
	count := 0
	cursor := bucket.Cursor()
	for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
		count++
	}
	for count > maxRecords {
		var removable []byte
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var record Record
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode audit record for retention: %w", err)
			}
			if record.Outcome != OutcomeStarted {
				removable = append([]byte(nil), key...)
				break
			}
		}
		if removable == nil {
			break
		}
		if err := bucket.Delete(removable); err != nil {
			return fmt.Errorf("delete old audit record: %w", err)
		}
		count--
	}
	return nil
}
