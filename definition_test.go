package jobs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jobs "github.com/plutoframework/pluto-jobs"
)

type orderNotice struct {
	OrderID string `json:"order_id"`
}

var errOrder = errors.New("order ID required")

func validateOrder(payload orderNotice) error {
	if payload.OrderID == "" {
		return errOrder
	}

	return nil
}

func TestTypedDefinitionBoundaries(t *testing.T) {
	definition, err := jobs.NewDefinition("order.created", validateOrder)
	if err != nil {
		t.Fatal(err)
	}

	tenant := "tenant-a"

	request, err := definition.Prepare(
		orderNotice{OrderID: "order-1"},
		jobs.Options{
			TenantID:    &tenant,
			AvailableAt: time.Now(),
			MaxAttempts: 3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	calls := 0

	var (
		delivered        jobs.Delivery
		deliveredPayload orderNotice
		deliveredTenant  string
	)

	handler, err := definition.Bind(
		func(
			_ context.Context,
			delivery jobs.Delivery,
			payload orderNotice,
		) error {
			calls++

			delivered = delivery
			deliveredPayload = payload

			if delivery.TenantID != nil {
				deliveredTenant = *delivery.TenantID
				*delivery.TenantID = "mutated"
			}

			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if definition.Kind() != request.Kind {
		t.Fatal("registration kind mismatch")
	}

	job := jobs.Job{Request: request, Attempt: 1}
	if err := handler(t.Context(), job); err != nil {
		t.Fatal(err)
	}

	if delivered.ID != request.ID || delivered.Attempt != 1 ||
		delivered.MaxAttempts != 3 ||
		deliveredTenant != "tenant-a" ||
		deliveredPayload.OrderID != "order-1" {
		t.Fatal("delivery metadata or payload lost")
	}

	if tenant != "tenant-a" || *job.TenantID != "tenant-a" {
		t.Fatal("delivery aliases stored tenant")
	}

	for _, tc := range []struct {
		name    string
		kind    jobs.Kind
		payload string
	}{
		{"different-kind", "shipment.created", `{"order_id":"order-1"}`},
		{"unknown-field", request.Kind, `{"order_id":"order-1","admin":true}`},
		{"domain-invalid", request.Kind, `{"order_id":""}`},
		{"trailing-value", request.Kind, `{"order_id":"order-1"} {}`},
		{"malformed", request.Kind, `{`},
		{"invalid-utf8", request.Kind, "{\"order_id\":\"\xff\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := job
			invalid.Kind = tc.kind
			invalid.Payload = []byte(tc.payload)
			err := handler(t.Context(), invalid)

			var permanent interface{ Unwrap() error }
			if err == nil || !errors.Is(err, jobs.ErrInvalid) ||
				!errors.As(err, &permanent) {
				t.Fatalf("not rejected: %v", err)
			}

			if tc.name == "domain-invalid" && !errors.Is(err, errOrder) {
				t.Fatalf("domain cause lost: %v", err)
			}

			if calls != 1 {
				t.Fatal("invalid delivery invoked side effects")
			}
		})
	}

	_, err = definition.Prepare(orderNotice{}, jobs.Options{})
	if !errors.Is(err, errOrder) || !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("prepare validation: %v", err)
	}

	if strings.Contains(err.Error(), errOrder.Error()) {
		t.Fatal("raw validation message exposed")
	}
}

func TestTypedExportDefinitionAndErrors(t *testing.T) {
	type exportReady struct {
		ExportID string `json:"export_id"`
		Rows     int    `json:"rows"`
	}

	definition, err := jobs.NewDefinition(
		"export.ready",
		func(payload exportReady) error {
			if payload.ExportID == "" || payload.Rows < 1 ||
				payload.Rows > 1000 {
				return jobs.ErrInvalid
			}

			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	transient := errors.New("delivery unavailable")

	handler, err := definition.Bind(
		func(context.Context, jobs.Delivery, exportReady) error {
			return transient
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	request, err := definition.Prepare(
		exportReady{ExportID: "report-1", Rows: 1000},
		jobs.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := handler(
		t.Context(),
		jobs.Job{Request: request, Attempt: 2},
	); !errors.Is(
		err,
		transient,
	) {
		t.Fatalf("retry error changed: %v", err)
	}

	if _, err := definition.Prepare(
		exportReady{ExportID: "report-1", Rows: 1001},
		jobs.Options{},
	); !errors.Is(
		err,
		jobs.ErrInvalid,
	) {
		t.Fatalf("export bound: %v", err)
	}

	var zero jobs.Definition[exportReady]
	if _, err := zero.Prepare(
		exportReady{},
		jobs.Options{},
	); !errors.Is(
		err,
		jobs.ErrInvalid,
	) {
		t.Fatal(err)
	}

	if _, err := zero.Bind(
		func(context.Context, jobs.Delivery, exportReady) error { return nil },
	); !errors.Is(
		err,
		jobs.ErrInvalid,
	) {
		t.Fatal(err)
	}

	if _, err := jobs.NewDefinition[exportReady](
		"export.ready",
		nil,
	); !errors.Is(
		err,
		jobs.ErrInvalid,
	) {
		t.Fatal(err)
	}

	if _, err := definition.Bind(nil); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatal(err)
	}
}
