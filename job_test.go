package jobs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T) Request {
	t.Helper()

	r, err := NewRequest(
		"email.welcome",
		struct{ Name string }{"synthetic"},
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func TestRequestValidation(t *testing.T) {
	for name, change := range map[string]func(*Request){
		"id":                func(r *Request) { r.ID = "bad" },
		"kind":              func(r *Request) { r.Kind = "bad kind" },
		"oversize":          func(r *Request) { r.Payload = json.RawMessage(strings.Repeat(" ", MaxPayloadBytes+1)) },
		"json":              func(r *Request) { r.Payload = json.RawMessage("{") },
		"zero attempts":     func(r *Request) { r.MaxAttempts = 0 },
		"infinite attempts": func(r *Request) { r.MaxAttempts = MaxAttempts + 1 },
		"time":              func(r *Request) { r.AvailableAt = time.Time{} },
		"tenant":            func(r *Request) { tenant := " "; r.TenantID = &tenant },
	} {
		t.Run(name, func(t *testing.T) {
			r := request(t)
			change(&r)

			if r.Validate() == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}

	tenant := "tenant-a"

	r, err := NewRequest("email", 1, Options{TenantID: &tenant})
	if err != nil {
		t.Fatal(err)
	}

	tenant = "changed"

	if *r.TenantID != "tenant-a" || r.ID == request(t).ID {
		t.Fatal("metadata alias or duplicate ID")
	}

	if _, err := NewRequest("email", make(chan int), Options{}); err == nil {
		t.Fatal("accepted non-JSON payload")
	}

	if _, err := Decode[int](Job{Request: r, Attempt: 0}); err == nil {
		t.Fatal("accepted invalid attempt")
	}
}

func FuzzRequest(f *testing.F) {
	f.Add("email", []byte(`{"id":1}`), 2, "tenant")
	f.Fuzz(
		func(t *testing.T, kind string, payload []byte, attempts int, tenant string) {
			r := Request{
				ID:          NewID(),
				Kind:        Kind(kind),
				Payload:     payload,
				MaxAttempts: attempts,
				TenantID:    &tenant,
				AvailableAt: time.Now(),
			}
			if r.Validate() != nil {
				return
			}

			if !json.Valid(payload) || len(payload) > MaxPayloadBytes ||
				attempts < 1 ||
				attempts > MaxAttempts ||
				len(kind) == 0 ||
				len(kind) > 128 ||
				strings.TrimSpace(tenant) == "" ||
				len(tenant) > 256 {
				t.Fatal("validation invariant violated")
			}
		},
	)
}
