<p align="center">
  <img src="https://raw.githubusercontent.com/plutoframework/pluto/main/assets/readme/banner.svg" alt="Pluto — a small framework, wide open possibilities" width="100%" />
</p>

# Pluto Jobs

Commit the work. Deliver the side effect.

[Website](https://plutoframework.github.io/pluto-site/) · [Documentation](https://plutoframework.github.io/pluto-site/docs/backend/jobs/) · [CLI demo](https://github.com/plutoframework/pluto/blob/main/docs/DEMO.md) · [First issues](https://github.com/plutoframework/pluto/blob/main/CONTRIBUTING.md#starter-tasks)

## What you get

- Typed jobs and explicit handlers for application-owned delivery.
- Same-transaction PostgreSQL outbox enqueue with finite retries.
- Expiring leases and fenced completion for at-least-once workers.

## Get started

### 1. Install

From a Go **1.25.5+** module:

```sh
go get github.com/plutoframework/pluto-jobs@v0.1.1
go get github.com/plutoframework/pluto-jobs/stores/postgres@v0.1.0
```

The core is standard-library-only; the PostgreSQL adapter is a separate module.

### 2. Create a typed request

Save this as `cmd/job/main.go`, then run `go run ./cmd/job`:

```go
package main

import (
    "fmt"

    jobs "github.com/plutoframework/pluto-jobs"
)

func main() {
    request, err := jobs.NewRequest(
        "purchase.submitted",
        struct {
            PurchaseID string `json:"purchase_id"`
        }{PurchaseID: "purchase-42"},
        jobs.Options{MaxAttempts: 5},
    )
    if err != nil {
        panic(err)
    }

    fmt.Println(request.Kind)
}
```

This creates a request; nothing is delivered until you supply storage and a worker.

### 3. Commit and deliver

Apply the [adapter schema](stores/postgres/schema.sql), register your handlers and
use `EnqueueTx` in the same transaction as the business write. Start with the
[transactional example](stores/postgres/examples/transactional/main.go) and its
[example schema](stores/postgres/examples/transactional/schema.sql). No email or webhook
client is hidden in the framework.

## Read further

- [API and integration reference](docs/REFERENCE.md)
- [Field guide](https://plutoframework.github.io/pluto-site/docs/backend/jobs/)
- [Pluto ecosystem](https://github.com/plutoframework/pluto#the-ecosystem)

## Boundaries

Delivery is at least once. Handlers must tolerate duplicates and cooperate with cancellation. Applications own migrations, tenant policy, retention and cleanup; no exactly-once guarantee or heartbeat renewal is implied.

## Contribute

Start with [CONTRIBUTING.md](CONTRIBUTING.md), then choose a [good first issue](https://github.com/plutoframework/pluto/blob/main/CONTRIBUTING.md#starter-tasks).

MIT licensed. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
