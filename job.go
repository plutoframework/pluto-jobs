// Package jobs implements durable, at-least-once application jobs.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxPayloadBytes = 64 * 1024
	MaxAttempts     = 100
)

type Kind string

type Options struct {
	TenantID    *string
	AvailableAt time.Time
	MaxAttempts int
}

// Request is validated at the persistence boundary, even when manually built.
type Request struct {
	ID          string
	Kind        Kind
	Payload     json.RawMessage
	TenantID    *string
	AvailableAt time.Time
	MaxAttempts int
}

// Job contains stored metadata. TenantID is explicit application metadata, not
// an authorization decision. ID remains stable across every delivery attempt.
type Job struct {
	Request
	Attempt int
}

// Claim tokens change on every acquisition. Never substitute a worker ID.
type Claim struct {
	Job   Job
	Token string
}

var (
	ErrInvalid   = errors.New("jobs: invalid request or stored metadata")
	ErrNoJob     = errors.New("jobs: no available job")
	ErrLeaseLost = errors.New("jobs: claim expired or replaced")
)

// NewID generates a random 128-bit identifier without external dependencies.
func NewID() string {
	var b [16]byte

	// crypto/rand.Read cannot fail on supported Go platforms.
	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

func validID(s string) bool {
	if len(s) != 32 {
		return false
	}

	_, err := hex.DecodeString(s)

	return err == nil && strings.ToLower(s) == s
}

func validKind(k Kind) bool {
	if len(k) == 0 || len(k) > 128 {
		return false
	}

	for _, c := range k {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' ||
			c == '.' ||
			c == '_' ||
			c == '-'
		if !allowed {
			return false
		}
	}

	return true
}

func (r Request) Validate() error {
	if !validID(r.ID) || !validKind(r.Kind) || len(r.Payload) == 0 ||
		len(r.Payload) > MaxPayloadBytes ||
		!json.Valid(r.Payload) ||
		r.MaxAttempts < 1 ||
		r.MaxAttempts > MaxAttempts ||
		r.AvailableAt.IsZero() ||
		r.AvailableAt.Year() < 1 ||
		r.AvailableAt.Year() > 9999 {
		return ErrInvalid
	}

	if r.TenantID != nil &&
		(strings.TrimSpace(*r.TenantID) == "" || len(*r.TenantID) > 256 || !utf8.ValidString(*r.TenantID) || strings.ContainsRune(*r.TenantID, 0)) {
		return ErrInvalid
	}

	return nil
}

func (j Job) Validate() error {
	if err := j.Request.Validate(); err != nil {
		return err
	}

	if j.Attempt < 1 || j.Attempt > j.MaxAttempts {
		return ErrInvalid
	}

	return nil
}

// NewRequest serializes typed application data. Construct once and retain ID
// when the transaction outcome is uncertain; duplicate IDs are rejected.
func NewRequest[T any](kind Kind, payload T, options Options) (Request, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Request{}, ErrInvalid
	}

	if options.MaxAttempts == 0 {
		options.MaxAttempts = 5
	}

	if options.AvailableAt.IsZero() {
		options.AvailableAt = time.Now().UTC()
	}

	r := Request{
		ID:          NewID(),
		Kind:        kind,
		Payload:     data,
		TenantID:    options.TenantID,
		AvailableAt: options.AvailableAt,
		MaxAttempts: options.MaxAttempts,
	}
	if r.TenantID != nil {
		tenant := *r.TenantID
		r.TenantID = &tenant
	}

	return r, r.Validate()
}

type Enqueuer interface {
	Enqueue(context.Context, Request) error
}

func Enqueue[T any](
	ctx context.Context,
	store Enqueuer,
	kind Kind,
	payload T,
	options Options,
) (string, error) {
	r, err := NewRequest(kind, payload, options)
	if err != nil {
		return "", err
	}

	if err = store.Enqueue(ctx, r); err != nil {
		return "", err
	}

	return r.ID, nil
}

func Decode[T any](job Job) (T, error) {
	var value T
	if err := job.Validate(); err != nil {
		return value, err
	}

	if err := json.Unmarshal(job.Payload, &value); err != nil {
		return value, ErrInvalid
	}

	return value, nil
}
