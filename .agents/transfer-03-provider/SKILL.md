---
name: transfer-03-provider
description: "Stage 3 of transfer-101: durable mock bank, transfer dispatch, provider idempotency, and ambiguous HTTP 504 simulation. Use after idempotency has been reviewed."
---

# Transfer 03: Provider

## Procedure and stage boundaries

- Inspect the actual repository, applicable `AGENTS.md` instructions, local skills, contracts, schema, tests, and user changes. Do not assume previous stages are available based on conversation history.
- The agent implements; the user reviews the design, code, and test results. Determine the scope, acceptance criteria, and required tests before editing. Do not ask the user to write code or answer quizzes.
- Prerequisite: stages 1 and 2 are available and have been reviewed. Otherwise, report the gaps and stop without automatically implementing another stage. Explain any supporting changes included in scope.
- Implement this stage only. Creating or reviewing a skill does not authorize running the application. Use Go 1.26.0 or later, PostgreSQL, LoB, the standard library, and a PostgreSQL driver/additional dependencies only as needed.
- Avoid ORMs, HTTP frameworks, generic repositories, DI containers, excessive layers, abstractions without concrete needs, Redis, message brokers, Kubernetes, and integrations with real banks.
- Run relevant checks and report actual results, including failures and checks that were not run, with reasons. Do not claim production readiness or fabricate benchmarks.
- Provide changed files, design rationale, limitations, and a review checklist. Stop before the next stage for review. Do not automatically publish or push.

## Implementation scope

Build an HTTP mock bank with its own durable PostgreSQL state, isolated from application tables through a separate schema or database. The application must determine bank outcomes through HTTP rather than reading mock bank tables directly. In-memory state is allowed only for test coordination.

Implement submission and basic inquiry so demo outcomes can be observed. Scheduled inquiry, durable retries, worker leases, and automatic recovery belong to stage 4. Bank reports belong to stage 5.

## Durable mock bank

- Use `(provider_account_id, provider_reference)` as the durable idempotency key. Concurrent identical submissions must produce one bank operation. Reject different payloads under the same key without introducing new side effects.
- Persist the operation/outcome atomically before returning a final response or simulating a transport error. Keys and outcomes must survive mock process restarts. Document their retention policy. Application uniqueness constraints do not replace this guarantee.
- Provide explicit, reproducible scenarios: final success, final rejection, HTTP 200 accepted/pending, bank success followed by an HTTP 504 response, and timeout/HTTP 500 with an uncertain outcome. Separate transport status from business status.
- Use delay/barrier controls for test scenarios, with deadlines as safeguards. Do not rely on random chaos to prove the main cases. Scenario controls are for local demos/tests only.
- Inquiry must use the mock bank's durable state. Define `not found` semantics. Absence does not automatically mean final failure or permission to resubmit.

## Application dispatch

- Use the existing transaction, bank integration account, and provider reference from stage 2. Persist them before contacting the bank. Do not generate a new reference for resubmission or inquiry.
- Atomically claim a `PENDING` transfer, move it to `PROCESSING`, record the attempt, and commit before the HTTP call. Do not hold database locks or transactions during HTTP calls.
- Bound timeouts with context. Interpret both the HTTP status and the provider contract body: HTTP 200 pending remains `PROCESSING`; timeouts, HTTP 504/500, malformed bodies, and disconnected connections remain nonfinal. Only valid final evidence may produce `SUCCESS` or `FAILED`.
- Apply outcomes through conditional updates so delayed responses cannot regress final statuses. Record contradictory final evidence for review rather than automatically overwriting the outcome. Use only the four agreed transfer statuses.
- Duplicate create requests must not produce a second bank operation. Concurrent dispatch requires a single claim winner. Protection across crash windows still depends on provider idempotency.
- Use an explicit local dispatch mechanism. An untracked goroutine must not be the only record of pending work. Document that a crash after claiming may leave a transfer in `PROCESSING` until recovery is implemented.
- Obtain the merchant from the authenticated context. Amounts remain integer IDR, and the funding source comes from configuration. Do not log credentials or full account numbers.

## Acceptance criteria

- [ ] Restarting the mock preserves keys/outcomes. Concurrent identical submissions produce one durable operation.
- [ ] Provider payload conflicts are rejected without overwriting data, and integration account scoping is correct.
- [ ] The transaction and provider reference are committed before the mock receives the request.
- [ ] Bank success followed by an HTTP 504 response leaves the application in `PROCESSING`; mock inquiry shows the bank's final success.
- [ ] HTTP 200 pending and transport errors do not cause false finality. Final business rejection is supported by evidence.
- [ ] Claiming does not hold locks throughout the HTTP call, and delayed responses cannot regress final statuses.

## Tests and checks

Use real PostgreSQL for mock constraint/concurrency tests and application claiming tests. Test submission/inquiry, parallel submissions, conflicts, account scoping, and mock restarts without deleting data. Use barriers/channels/hooks to prove commit-before-call behavior and control delayed responses. Do not use `time.Sleep` to wait for asynchronous results.

Test every transport/body scenario, duplicate create requests, concurrent dispatch, timeout after the bank commits, and delayed outcomes. Count durable bank operations separately from HTTP attempts. Run formatting, build, `go vet`, relevant tests, and applicable race detector checks. Record commands, exit statuses, actual results, and checks that were not run.

## Review checklist and stopping point

- [ ] Explain changed files, mock contracts, status/body mapping, and transaction boundaries.
- [ ] Provide a reproducible ambiguous-504 demo and evidence of one bank operation per key.
- [ ] Clearly state limitations before automatic recovery and funds reservation are implemented.
- [ ] Stop for review. Proceed to `transfer-04-recovery` only after user review.