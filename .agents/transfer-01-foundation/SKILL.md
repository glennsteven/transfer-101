---
name: transfer-01-foundation
description: "Stage 1 of transfer-101: set up Go and PostgreSQL, define API/provider contracts, establish the status model, and organize code using Locality of Behavior. Use this stage to build the foundation before implementing idempotency."
---

# Transfer 01: Foundation

## Procedure and stage boundaries

- Inspect the actual repository, applicable `AGENTS.md` instructions, local skills, configuration, tests, and user changes. Do not assume code or results from previous conversations are available.
- The agent implements; the user reviews the design, code, and test results. Make routine decisions independently. Do not turn the process into a quiz or ask the user to write code.
- Before editing, determine the scope, acceptance criteria, and required tests based on the current repository.
- Implement this stage only. A request to create or review a skill does not authorize running its implementation. Stop before the next stage for user review.
- Use Go 1.26.0 or later, PostgreSQL, and the standard library. A PostgreSQL driver is allowed. Explain any additional dependency that is genuinely necessary.
- Apply Locality of Behavior (LoB): group handlers, SQL, business logic, and tests by feature. Avoid ORMs, HTTP frameworks, generic repositories, DI containers, excessive layers, and abstractions without concrete needs. Do not add Redis, message brokers, Kubernetes, or integrations with real banks.
- Run relevant checks and report actual results, including failures and checks that were not run, with reasons. Do not claim production readiness or fabricate benchmarks.
- Provide the changed files, design rationale, limitations, and a review checklist. Do not automatically publish or push.

## Implementation scope

Set up the Go module, validated configuration, `net/http` server, PostgreSQL connectivity, startup/shutdown behavior, and instructions for running PostgreSQL locally. Simple health/readiness endpoints may demonstrate that the foundation works. Establish the versions and tooling actually available. Do not lower the minimum Go version to make checks pass.

Use a feature-based structure, such as `internal/transfer`, `internal/mockbank`, and eventually `internal/reconciliation`, with `cmd` for entry points and `migrations` for SQL. Adapt to the actual repository. Do not create empty packages for future stages or add layers merely to follow a pattern.

Document the API/provider contracts and the design decisions below. Implementing the transfer schema, create/get operations, dispatch, workers, or funds handling is outside this stage.

## Required design decisions

- Use only these transfer statuses: `PENDING`, `PROCESSING`, `SUCCESS`, and `FAILED`. `PENDING` means persisted and awaiting dispatch. `PROCESSING` means processing has started, including cases where the external outcome remains uncertain. `SUCCESS` and `FAILED` require clear final evidence.
- The general flow is `PENDING -> PROCESSING -> SUCCESS/FAILED`. Timeouts, HTTP 504/500 responses, disconnected connections, and malformed responses do not prove final failure. Interpret HTTP 200 together with the response body and provider contract; accepted/pending outcomes remain nonfinal. Evidence contradicting a final status becomes a review finding rather than triggering an automatic overwrite.
- The API contract must define create/get routes, request/response formats, HTTP status semantics, validation, and merchant isolation. Obtain `merchant_id` from the authenticated context, not the request body or an arbitrary identity header. Local credentials mapped to merchants are allowed for demo authentication and must be labeled accordingly.
- The MVP supports only IDR, represented as positive integer rupiah amounts that fit within `int64`, with one configured funding source per merchant. Represent `bank_code` and `account_number` as strings to preserve leading zeros. Define normalization before storage/comparison. Do not use floating point.
- Enforce `UNIQUE(merchant_id, reference_id)`. The same key with the same payload returns the existing transaction. The same key with a different payload returns `409`, regardless of its previous status. Compare `amount`, `bank_code`, and `account_number` after contract-defined validation and normalization.
- Use `INSERT ... ON CONFLICT (merchant_id, reference_id) DO NOTHING RETURNING id`. If no row is returned, retrieve the existing transaction with a separate `SELECT`. Do not substitute check-then-insert or a single-snapshot insert/select CTE.
- Persist the transaction, its bank integration account, and its `provider_reference` before contacting the bank. The reference must be unique within the bank integration account's scope. Retries and inquiries must reuse the existing account and reference. Local uniqueness constraints do not replace provider idempotency.
- The mock bank contract must define submission, inquiry, reports, business statuses, payload conflicts, reference scope, deduplication guarantees and retention, and the meaning of `not found`. An inquiry result of `not found` does not necessarily make resubmission safe.
- Do not hold database locks during HTTP calls. Document commit/HTTP boundaries and crash windows. Exhausting retries marks the transaction as requiring review, not automatically `FAILED`. Do not add a transfer status for review.
- Distinguish per-transaction inquiry from report reconciliation. Asynchronous tests must use deterministic synchronization rather than `time.Sleep` to wait for results. Test constraints and concurrency against real PostgreSQL.

## Acceptance criteria

- [ ] The minimum Go version, configuration, startup/shutdown, and PostgreSQL readiness are documented and verifiable.
- [ ] Example configuration contains only demo credentials and no real secrets.
- [ ] API/provider contracts, the four statuses, final evidence, idempotency, reference scope, and merchant isolation are consistent.
- [ ] The initial structure follows LoB, and every dependency has a concrete justification.
- [ ] Implementation stops at the foundation. Work belonging to stages 2 through 7 is clearly identified as not yet implemented.

## Tests and checks

Run formatting, build, `go vet`, relevant configuration/health tests, and a startup/shutdown smoke check with local PostgreSQL. Use deadlines for connection failures and shutdown. Record commands, actual versions, exit statuses, and observed results. If tooling is unavailable, report what remains untested. Do not claim transfer endpoints have been tested before they are implemented.

## Review checklist and stopping point

- [ ] The list of changed files, structure/dependency rationale, and contracts are available for review.
- [ ] Verification evidence, limitations, and reproduction commands are included.
- [ ] Stop for review. Proceed to `transfer-02-idempotency` only after user review.