package jobs

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Code is a fixed diagnostic vocabulary. Raw errors and panic values are never
// persisted or logged by this library.
type Code string

const (
	CodeHandler      Code = "handler_error"
	CodePanic        Code = "handler_panic"
	CodePermanent    Code = "permanent"
	CodeUnregistered Code = "unregistered"
	CodeInvalid      Code = "invalid"
	CodeTimeout      Code = "timeout"
	CodeCanceled     Code = "canceled"
)

func (c Code) Valid() bool {
	switch c {
	case CodeHandler,
		CodePanic,
		CodePermanent,
		CodeUnregistered,
		CodeInvalid,
		CodeTimeout,
		CodeCanceled:
		return true
	default:
		return false
	}
}

type Store interface {
	Claim(context.Context, time.Duration) (Claim, error)
	Ack(context.Context, Claim) error
	Retry(context.Context, Claim, time.Duration, Code) error
	Fail(context.Context, Claim, Code) error
}

type Handler func(context.Context, Job) error

type permanentError struct{ err error }

func (e permanentError) Error() string { return "jobs: permanent handler failure" }
func (e permanentError) Unwrap() error { return e.err }
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return permanentError{err: err}
}

type Config struct {
	Concurrency    int
	PollInterval   time.Duration
	LeaseDuration  time.Duration
	HandlerTimeout time.Duration
	StoreTimeout   time.Duration
	RetryBase      time.Duration
	RetryMax       time.Duration
}

func DefaultConfig() Config {
	return Config{
		Concurrency:    4,
		PollInterval:   time.Second,
		LeaseDuration:  time.Minute,
		HandlerTimeout: 30 * time.Second,
		StoreTimeout:   5 * time.Second,
		RetryBase:      time.Second,
		RetryMax:       time.Hour,
	}
}

type Worker struct {
	store    Store
	handlers map[Kind]Handler
	config   Config
}

// NewWorker snapshots registrations. Handlers must support concurrent calls,
// honor cancellation, and use job.ID for external idempotency where possible.
func NewWorker(
	store Store,
	handlers map[Kind]Handler,
	config Config,
) (*Worker, error) {
	if store == nil || config.Concurrency < 1 || config.Concurrency > 256 ||
		config.PollInterval <= 0 ||
		config.StoreTimeout <= 0 ||
		config.HandlerTimeout <= 0 ||
		config.LeaseDuration < time.Millisecond ||
		config.LeaseDuration <= config.HandlerTimeout ||
		config.LeaseDuration > 24*time.Hour ||
		config.StoreTimeout > time.Hour ||
		config.StoreTimeout >= (config.LeaseDuration-config.HandlerTimeout)/2 ||
		config.RetryMax > 24*time.Hour ||
		config.RetryBase <= 0 ||
		config.RetryMax < config.RetryBase {
		return nil, ErrInvalid
	}

	copied := make(map[Kind]Handler, len(handlers))
	for kind, handler := range handlers {
		if !validKind(kind) || handler == nil {
			return nil, ErrInvalid
		}

		copied[kind] = handler
	}

	return &Worker{store: store, handlers: copied, config: config}, nil
}

// Run blocks until canceled or a store operation fails. Shutdown cancels active
// handlers and waits for them; Go cannot forcibly stop an uncooperative handler.
// Store errors are intentionally sanitized, since database errors can contain data.
func (w *Worker) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	failures := make(chan error, w.config.Concurrency)

	var wg sync.WaitGroup
	for range w.config.Concurrency {
		wg.Go(func() {
			if err := w.loop(ctx); err != nil {
				failures <- err

				cancel()
			}
		})
	}

	wg.Wait()
	close(failures)

	for err := range failures {
		return err
	}

	return ctx.Err()
}

func (w *Worker) loop(ctx context.Context) error {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, w.config.StoreTimeout)
		claim, err := w.store.Claim(claimCtx, w.config.LeaseDuration)

		cancel()

		if errors.Is(err, ErrNoJob) {
			timer := time.NewTimer(w.config.PollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()

				return nil
			case <-timer.C:
			}

			continue
		}

		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return errors.New("jobs: claim failed")
		}

		if err = w.deliver(ctx, claim); err != nil {
			return err
		}
	}

	return nil
}

func (w *Worker) deliver(ctx context.Context, claim Claim) error {
	handlerCtx, cancel := context.WithTimeout(ctx, w.config.HandlerTimeout)

	code, retry := w.invoke(handlerCtx, claim.Job)
	if retry && handlerCtx.Err() != nil {
		code, retry = CodeTimeout, true
		if ctx.Err() != nil {
			code = CodeCanceled
		}
	}

	cancel()

	// Settlement gets a bounded context even during graceful shutdown. If it
	// fails, the durable lease expires and the consumed attempt is recovered.
	settleCtx, stop := context.WithTimeout(
		context.WithoutCancel(ctx),
		w.config.StoreTimeout,
	)
	defer stop()

	var err error

	switch {
	case code == "":
		err = w.store.Ack(settleCtx, claim)
	case !retry || claim.Job.Attempt >= claim.Job.MaxAttempts:
		err = w.store.Fail(settleCtx, claim, code)
	default:
		err = w.store.Retry(
			settleCtx,
			claim,
			w.backoff(claim.Job.Attempt),
			code,
		)
	}

	if err != nil && !errors.Is(err, ErrLeaseLost) {
		return errors.New("jobs: settlement failed")
	}

	return nil
}

func (w *Worker) invoke(ctx context.Context, job Job) (code Code, retry bool) {
	defer func() {
		if recover() != nil {
			code, retry = CodePanic, true
		}
	}()

	if job.Validate() != nil {
		return CodeInvalid, false
	}

	handler, ok := w.handlers[job.Kind]
	if !ok {
		return CodeUnregistered, false
	}

	if ctx.Err() != nil {
		return CodeCanceled, true
	}

	err := handler(ctx, job)
	if err == nil {
		return "", false
	}

	var permanent permanentError
	if errors.As(err, &permanent) {
		return CodePermanent, false
	}

	return CodeHandler, true
}

func (w *Worker) backoff(attempt int) time.Duration {
	delay := w.config.RetryBase
	for n := 1; n < attempt; n++ {
		if delay > w.config.RetryMax/2 {
			return w.config.RetryMax
		}

		delay *= 2
	}

	return min(delay, w.config.RetryMax)
}
