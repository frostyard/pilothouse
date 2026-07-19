// Package jobs provides durable storage for maintenance jobs.
package jobs

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
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusUnknown   = "unknown"

	maxListLimit = 100
)

var (
	jobsBucket           = []byte("jobs")
	ErrNotFound          = errors.New("job not found")
	ErrInvalidTransition = errors.New("invalid job status transition")
)

// Attempt contains the non-sensitive fields recorded when a job is queued.
type Attempt struct {
	Action   string
	AuditID  uint64
	Resource string
	Username string
	UID      int
}

// Job is the durable history of a maintenance operation.
type Job struct {
	ID             uint64     `json:"id"`
	AuditID        uint64     `json:"audit_id"`
	Action         string     `json:"action"`
	Resource       string     `json:"resource"`
	Username       string     `json:"username"`
	UID            int        `json:"uid"`
	Status         string     `json:"status"`
	ErrorCategory  string     `json:"error_category,omitempty"`
	RebootRequired bool       `json:"reboot_required"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	DurationMS     int64      `json:"duration_ms"`
}

// Filter selects jobs by exact action and status. Limit defaults to 100.
type Filter struct {
	Limit  int
	Action string
	Status string
}

// Store is a durable maintenance job store.
type Store struct {
	db         *bolt.DB
	maxRecords int
}

// Open opens or creates a job store and recovers interrupted jobs.
func Open(path string, maxRecords int) (*Store, error) {
	if maxRecords <= 0 {
		return nil, fmt.Errorf("maxRecords must be positive")
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open jobs database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure jobs database: %w", err)
	}

	store := &Store{db: db, maxRecords: maxRecords}
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(jobsBucket)
		if err != nil {
			return fmt.Errorf("create jobs bucket: %w", err)
		}

		now := time.Now().UTC()
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return fmt.Errorf("decode job: %w", err)
			}
			if !activeStatus(job.Status) {
				continue
			}
			finishJob(&job, now, StatusUnknown, "", false)
			encoded, err := json.Marshal(job)
			if err != nil {
				return fmt.Errorf("encode recovered job: %w", err)
			}
			if err := bucket.Put(key, encoded); err != nil {
				return fmt.Errorf("recover job: %w", err)
			}
		}

		return trimOldest(bucket, maxRecords)
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize jobs database: %w", err)
	}

	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Enqueue durably records a queued job before returning it.
func (s *Store) Enqueue(ctx context.Context, attempt Attempt) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}

	job := Job{
		Action:    attempt.Action,
		AuditID:   attempt.AuditID,
		Resource:  attempt.Resource,
		Username:  attempt.Username,
		UID:       attempt.UID,
		Status:    StatusQueued,
		CreatedAt: time.Now().UTC(),
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(jobsBucket)
		id, err := bucket.NextSequence()
		if err != nil {
			return fmt.Errorf("allocate job ID: %w", err)
		}
		job.ID = id
		encoded, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("encode job: %w", err)
		}
		if err := bucket.Put(jobKey(id), encoded); err != nil {
			return fmt.Errorf("store job: %w", err)
		}
		return trimOldest(bucket, s.maxRecords)
	})
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

// Start marks a queued job as running.
func (s *Store) Start(ctx context.Context, id uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return s.update(ctx, id, func(job *Job) error {
		if job.Status != StatusQueued {
			return fmt.Errorf("%w: cannot start job in status %q", ErrInvalidTransition, job.Status)
		}
		now := time.Now().UTC()
		job.Status = StatusRunning
		job.StartedAt = &now
		return nil
	})
}

// Complete records the final result of a running job.
func (s *Store) Complete(ctx context.Context, id uint64, status, errorCategory string, rebootRequired bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !finalStatus(status) {
		return fmt.Errorf("invalid final status %q", status)
	}

	return s.update(ctx, id, func(job *Job) error {
		if job.Status != StatusRunning {
			return fmt.Errorf("%w: cannot complete job in status %q", ErrInvalidTransition, job.Status)
		}
		finishJob(job, time.Now().UTC(), status, errorCategory, rebootRequired)
		return nil
	})
}

// List returns matching jobs newest first.
func (s *Store) List(ctx context.Context, filter Filter) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter.Limit < 0 {
		return nil, fmt.Errorf("limit must not be negative")
	}
	if filter.Status != "" && !validStatus(filter.Status) {
		return nil, fmt.Errorf("invalid status filter %q", filter.Status)
	}
	limit := filter.Limit
	if limit == 0 || limit > maxListLimit {
		limit = maxListLimit
	}

	jobs := make([]Job, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(jobsBucket).Cursor()
		for _, value := cursor.Last(); value != nil && len(jobs) < limit; _, value = cursor.Prev() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return fmt.Errorf("decode job: %w", err)
			}
			if filter.Action != "" && job.Action != filter.Action {
				continue
			}
			if filter.Status != "" && job.Status != filter.Status {
				continue
			}
			jobs = append(jobs, job)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// RebootRequiredSince reports whether a successful job after the supplied boot
// time installed changes that require reboot activation.
func (s *Store) RebootRequiredSince(ctx context.Context, since time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	required := false
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(jobsBucket).Cursor()
		for _, value := cursor.Last(); value != nil; _, value = cursor.Prev() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return fmt.Errorf("decode job: %w", err)
			}
			if job.Status == StatusSucceeded && job.RebootRequired && job.FinishedAt != nil && job.FinishedAt.After(since) {
				required = true
				break
			}
		}
		return nil
	})
	return required, err
}

func (s *Store) update(ctx context.Context, id uint64, change func(*Job) error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(jobsBucket)
		key := jobKey(id)
		value := bucket.Get(key)
		if value == nil {
			return ErrNotFound
		}
		var job Job
		if err := json.Unmarshal(value, &job); err != nil {
			return fmt.Errorf("decode job: %w", err)
		}
		if err := change(&job); err != nil {
			return err
		}
		encoded, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("encode job: %w", err)
		}
		if err := bucket.Put(key, encoded); err != nil {
			return fmt.Errorf("store job: %w", err)
		}
		return trimOldest(bucket, s.maxRecords)
	})
}

func finishJob(job *Job, finishedAt time.Time, status, errorCategory string, rebootRequired bool) {
	job.Status = status
	job.ErrorCategory = errorCategory
	job.RebootRequired = rebootRequired
	job.FinishedAt = &finishedAt
	job.DurationMS = 0
	if job.StartedAt != nil {
		job.DurationMS = finishedAt.Sub(*job.StartedAt).Milliseconds()
		if job.DurationMS < 0 {
			job.DurationMS = 0
		}
	}
}

func validStatus(status string) bool {
	return activeStatus(status) || finalStatus(status)
}

func activeStatus(status string) bool {
	return status == StatusQueued || status == StatusRunning
}

func finalStatus(status string) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusUnknown
}

func jobKey(id uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, id)
	return key
}

func trimOldest(bucket *bolt.Bucket, maxRecords int) error {
	count := bucket.Stats().KeyN
	for count > maxRecords {
		var removable []byte
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return fmt.Errorf("decode job for retention: %w", err)
			}
			if !activeStatus(job.Status) {
				removable = append([]byte(nil), key...)
				break
			}
		}
		if removable == nil {
			break
		}
		if err := bucket.Delete(removable); err != nil {
			return fmt.Errorf("delete old job: %w", err)
		}
		count--
	}
	return nil
}
