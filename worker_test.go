package jobs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type (
	settlement struct {
		state string
		code  Code
		delay time.Duration
	}
	testStore struct {
		claims     chan Claim
		settled    chan settlement
		claimError error
	}
)

func newTestStore() *testStore {
	return &testStore{
		claims:  make(chan Claim, 8),
		settled: make(chan settlement, 8),
	}
}

func (s *testStore) Claim(context.Context, time.Duration) (Claim, error) {
	if s.claimError != nil {
		return Claim{}, s.claimError
	}

	select {
	case c := <-s.claims:
		return c, nil
	default:
		return Claim{}, ErrNoJob
	}
}

func (s *testStore) Ack(context.Context, Claim) error {
	s.settled <- settlement{state: "succeeded"}
	return nil
}

func (s *testStore) Retry(
	_ context.Context,
	_ Claim,
	delay time.Duration,
	code Code,
) error {
	s.settled <- settlement{state: "pending", code: code, delay: delay}
	return nil
}

func (s *testStore) Fail(_ context.Context, _ Claim, code Code) error {
	s.settled <- settlement{state: "failed", code: code}
	return nil
}

func testConfig() Config {
	c := DefaultConfig()
	c.Concurrency = 1
	c.PollInterval = time.Millisecond
	c.HandlerTimeout = 20 * time.Millisecond
	c.StoreTimeout = 10 * time.Millisecond
	c.LeaseDuration = time.Second

	return c
}

func TestWorkerOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		handler Handler
		attempt int
		invalid bool
		state   string
		code    Code
	}{
		{
			name:    "success",
			handler: func(context.Context, Job) error { return nil },
			attempt: 1,
			state:   "succeeded",
		},
		{
			name:    "retry",
			handler: func(context.Context, Job) error { return errors.New("secret") },
			attempt: 1,
			state:   "pending",
			code:    CodeHandler,
		},
		{
			name:    "permanent",
			handler: func(context.Context, Job) error { return Permanent(errors.New("secret")) },
			attempt: 1,
			state:   "failed",
			code:    CodePermanent,
		},
		{
			name:    "panic",
			handler: func(context.Context, Job) error { panic("secret") },
			attempt: 1,
			state:   "pending",
			code:    CodePanic,
		},
		{
			name:    "exhausted panic",
			handler: func(context.Context, Job) error { panic("secret") },
			attempt: 5,
			state:   "failed",
			code:    CodePanic,
		},
		{
			name:    "unregistered",
			attempt: 1,
			state:   "failed",
			code:    CodeUnregistered,
		},
		{
			name:    "invalid",
			attempt: 1,
			invalid: true,
			state:   "failed",
			code:    CodeInvalid,
		},
		{
			name: "timeout",

			handler: func(ctx context.Context, _ Job) error {
				<-ctx.Done()

				return ctx.Err()
			},
			attempt: 1,
			state:   "pending",
			code:    CodeTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()

			r := request(t)
			if tc.invalid {
				r.Payload = []byte("bad")
			}

			handlers := map[Kind]Handler{}
			if tc.handler != nil {
				handlers[r.Kind] = tc.handler
			}

			w, err := NewWorker(s, handlers, testConfig())
			if err != nil {
				t.Fatal(err)
			}

			// Registrations are an immutable snapshot.
			delete(handlers, r.Kind)

			if err = w.deliver(
				context.Background(),
				Claim{
					Job:   Job{Request: r, Attempt: tc.attempt},
					Token: NewID(),
				},
			); err != nil {
				t.Fatal(err)
			}

			result := <-s.settled
			if result.state != tc.state || result.code != tc.code {
				t.Fatalf("got %+v", result)
			}

			if result.state == "pending" && result.delay <= 0 {
				t.Fatal("retry without backoff")
			}
		})
	}
}

func TestRunShutdownAndConcurrency(t *testing.T) {
	s := newTestStore()
	c := testConfig()
	c.Concurrency = 2
	started := make(chan struct{}, 2)

	var active, peak atomic.Int32

	handler := func(ctx context.Context, _ Job) error {
		n := active.Add(1)

		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}

		defer active.Add(-1)

		started <- struct{}{}

		<-ctx.Done()

		return ctx.Err()
	}

	r := request(t)
	for range 4 {
		s.claims <- Claim{Job: Job{Request: r, Attempt: 1}, Token: NewID()}
	}

	w, err := NewWorker(s, map[Kind]Handler{r.Kind: handler}, c)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}

	cancel()

	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown hung")
	}

	if active.Load() != 0 || peak.Load() != 2 {
		t.Fatal("concurrency or shutdown invariant")
	}

	for range 2 {
		if result := <-s.settled; result.code != CodeCanceled {
			t.Fatal(result)
		}
	}
}

func TestConfigBackoffAndSanitizedErrors(t *testing.T) {
	s := newTestStore()
	c := testConfig()

	w, err := NewWorker(s, nil, c)
	if err != nil {
		t.Fatal(err)
	}

	if w.backoff(1) != c.RetryBase || w.backoff(100) != c.RetryMax {
		t.Fatal("backoff bounds")
	}

	c.HandlerTimeout = c.LeaseDuration
	if _, err = NewWorker(s, nil, c); err == nil {
		t.Fatal("accepted unsafe deadline")
	}

	s.claimError = errors.New("password=secret")

	err = w.Run(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsanitized store failure")
	}

	if Permanent(nil) != nil {
		t.Fatal("nil permanent error")
	}
}

func TestCompletedOutcomeDuringShutdown(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		s := newTestStore()
		r := request(t)
		ctx, cancel := context.WithCancel(context.Background())
		handler := func(context.Context, Job) error {
			cancel()

			if permanent {
				return Permanent(errors.New("secret"))
			}

			return nil
		}

		w, err := NewWorker(s, map[Kind]Handler{r.Kind: handler}, testConfig())
		if err != nil {
			t.Fatal(err)
		}

		if err = w.deliver(
			ctx,
			Claim{Job: Job{Request: r, Attempt: 1}, Token: NewID()},
		); err != nil {
			t.Fatal(err)
		}

		result := <-s.settled

		want := "succeeded"
		if permanent {
			want = "failed"
		}

		if result.state != want {
			t.Fatalf("completed outcome changed by shutdown: %+v", result)
		}
	}
}
