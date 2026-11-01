package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Delivery supplies stored delivery metadata to a typed handler. TenantID is
// application metadata, not execution authority; re-establish current access in
// the operation. ID stays stable across retries and can key downstream
// deduping.
type Delivery struct {
	ID          string
	TenantID    *string
	Attempt     int
	MaxAttempts int
}

// Definition binds one event kind to its payload type and domain validation.
// It is immutable after construction. Validation must not perform I/O, mutate
// shared facts, or rely on ambient authority. Go cannot enforce callback
// purity.
// This is an additive boundary, not mandatory adoption for existing raw APIs.
type Definition[T any] struct {
	kind     Kind
	validate func(T) error
}

// NewDefinition requires explicit validation, including an explicit no-op where
// a payload has no domain invariants. It does not register a worker or perform
// I/O.
func NewDefinition[T any](
	kind Kind,
	validate func(T) error,
) (*Definition[T], error) {
	if !validKind(kind) || validate == nil {
		return nil, ErrInvalid
	}

	return &Definition[T]{kind: kind, validate: validate}, nil
}

// Kind is the explicit registration key for the definition's typed handler.
func (d *Definition[T]) Kind() Kind {
	if d == nil {
		return ""
	}

	return d.kind
}

// Prepare validates a typed payload before constructing a request. Construct
// once and retain its ID across an uncertain transaction outcome. Persistence
// must still validate Request and enforce atomic business mutation/enqueue.
func (d *Definition[T]) Prepare(payload T, options Options) (Request, error) {
	if d == nil || !validKind(d.kind) || d.validate == nil {
		return Request{}, ErrInvalid
	}

	if err := d.validate(payload); err != nil {
		return Request{}, invalidPayload{cause: err}
	}

	return NewRequest(d.kind, payload, options)
}

// Bind adapts a typed application operation to Handler. Before invocation it
// validates stored metadata, exact event kind, one JSON value, unknown fields
// for ordinary structs, and domain payload invariants. Invalid stored payloads
// are permanent failures; operation errors retain the existing retry semantics.
// Custom JSON unmarshaling retains encoding/json's own application semantics.
func (d *Definition[T]) Bind(
	operation func(context.Context, Delivery, T) error,
) (Handler, error) {
	if d == nil || !validKind(d.kind) || d.validate == nil || operation == nil {
		return nil, ErrInvalid
	}

	return func(ctx context.Context, job Job) error {
		if err := job.Validate(); err != nil {
			return Permanent(err)
		}

		if job.Kind != d.kind {
			return Permanent(ErrInvalid)
		}

		if !utf8.Valid(job.Payload) {
			return Permanent(ErrInvalid)
		}

		var payload T

		decoder := json.NewDecoder(bytes.NewReader(job.Payload))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&payload); err != nil {
			return Permanent(ErrInvalid)
		}

		if decoder.Decode(new(any)) != io.EOF {
			return Permanent(ErrInvalid)
		}

		if err := d.validate(payload); err != nil {
			return Permanent(invalidPayload{cause: err})
		}

		delivery := Delivery{
			ID:          job.ID,
			Attempt:     job.Attempt,
			MaxAttempts: job.MaxAttempts,
		}
		if job.TenantID != nil {
			tenant := *job.TenantID
			delivery.TenantID = &tenant
		}

		return operation(ctx, delivery, payload)
	}, nil
}

// Error strings remain bounded; callers can inspect validation causes with Is.
type invalidPayload struct{ cause error }

func (e invalidPayload) Error() string {
	return "jobs: invalid typed payload"
}
func (e invalidPayload) Unwrap() []error { return []error{ErrInvalid, e.cause} }
