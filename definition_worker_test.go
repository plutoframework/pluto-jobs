package jobs

import (
	"context"
	"errors"
	"testing"
)

func TestTypedDefinitionWorkerSettlement(t *testing.T) {
	type payload struct {
		ID string `json:"id"`
	}

	definition, err := NewDefinition("typed.notice", func(value payload) error {
		if value.ID == "" {
			return ErrInvalid
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name           string
		attempt        int
		invalid        bool
		operationError error
		state          string
		code           Code
	}{
		{name: "permanent-invalid", attempt: 1, invalid: true, state: "failed", code: CodePermanent},
		{name: "retry-operation", attempt: 1, operationError: errors.New("transient"), state: "pending", code: CodeHandler},
		{name: "finite-operation", attempt: 2, operationError: errors.New("transient"), state: "failed", code: CodeHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0

			handler, err := definition.Bind(
				func(context.Context, Delivery, payload) error { calls++; return tc.operationError },
			)
			if err != nil {
				t.Fatal(err)
			}

			request, err := definition.Prepare(
				payload{ID: "notice-1"},
				Options{MaxAttempts: 2},
			)
			if err != nil {
				t.Fatal(err)
			}

			if tc.invalid {
				request.Payload = []byte(`{"id":""}`)
			}

			store := newTestStore()

			worker, err := NewWorker(
				store,
				map[Kind]Handler{definition.Kind(): handler},
				testConfig(),
			)
			if err != nil {
				t.Fatal(err)
			}

			if err := worker.deliver(
				t.Context(),
				Claim{
					Job:   Job{Request: request, Attempt: tc.attempt},
					Token: NewID(),
				},
			); err != nil {
				t.Fatal(err)
			}

			settled := <-store.settled
			if settled.state != tc.state || settled.code != tc.code {
				t.Fatalf("settlement %+v", settled)
			}

			if tc.invalid && calls != 0 {
				t.Fatal("invalid payload executed operation")
			}

			if !tc.invalid && calls != 1 {
				t.Fatal("valid operation not invoked once")
			}
		})
	}
}
