package orders

import (
	"context"
	"errors"
	"testing"

	jobs "github.com/plutoframework/pluto-jobs"
)

type senderFunc func(context.Context, string, string, OrderCreated) error

func (send senderFunc) Send(
	ctx context.Context,
	id, tenant string,
	payload OrderCreated,
) error {
	return send(ctx, id, tenant, payload)
}

func TestDeliveryKeepsIdentityAndPropagatesFailure(t *testing.T) {
	tenant := "north"

	request, err := jobs.NewRequest(
		Created,
		OrderCreated{OrderID: "order-42"},
		jobs.Options{TenantID: &tenant},
	)
	if err != nil {
		t.Fatal(err)
	}

	unavailable := errors.New("delivery unavailable")
	calls := 0

	handler := Delivery(senderFunc(func(
		_ context.Context,
		id, actualTenant string,
		payload OrderCreated,
	) error {
		calls++

		if id != request.ID || actualTenant != tenant ||
			payload.OrderID != "order-42" {
			t.Error("delivery lost its stable identity or tenant")
		}

		return unavailable
	}))

	for range 2 {
		if err := handler(
			context.Background(),
			jobs.Job{Request: request, Attempt: 1},
		); !errors.Is(
			err,
			unavailable,
		) {
			t.Fatal("delivery error must reach worker retry accounting")
		}
	}

	if calls != 2 {
		t.Fatal("expected repeated delivery with the same key")
	}
}

func TestDeliveryRejectsMissingTenant(t *testing.T) {
	request, err := jobs.NewRequest(
		Created,
		OrderCreated{OrderID: "42"},
		jobs.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}

	called := false

	handler := Delivery(senderFunc(func(
		context.Context, string, string, OrderCreated,
	) error {
		called = true

		return nil
	}))

	if err := handler(
		context.Background(),
		jobs.Job{Request: request, Attempt: 1},
	); err == nil ||
		called {
		t.Fatal("missing tenant must fail before delivery")
	}
}
