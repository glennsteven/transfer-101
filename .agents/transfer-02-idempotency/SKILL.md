---
name: transfer-02-idempotency
description: "Stage 2 of transfer-101: PostgreSQL schema, transfer creation/retrieval, duplicate requests, and payload conflicts. Use after the foundation has been reviewed."
---

# Transfer 02: Idempotency

## Procedure and stage boundaries

- Inspect the actual repository, applicable `AGENTS.md` instructions, local skills, contracts, migrations, tests, and user changes. Do not assume previous-stage results are available based on conversation history.
- The agent implements; the user reviews the design, code, and test results. State the scope, acceptance criteria, and required tests before editing. Do not ask the user to write code or answer quizzes.
- Prerequisite: the stage 1 foundation exists and has been reviewed. Otherwise, report the gaps and stop. Do not automatically execute another stage. Explain any small supporting changes included in scope.
- Implement this stage only. Creating or reviewing a skill does not authorize running the application. Use Go 1.26.0 or later, PostgreSQL, LoB, the standard library, and a PostgreSQL driver/additional dependencies only as needed.
- Avoid ORMs, HTTP frameworks, generic repositories, DI containers, excessive layers, abstractions without concrete needs, Redis, message brokers, Kubernetes, and integrations with real banks.
- Run relevant checks and report actual results, including failures and checks that were not run, with reasons. Do not claim production readiness or fabricate benchmarks.
- Provide changed files, design rationale, limitations, and a review checklist. Stop before the next stage for review. Do not automatically publish or push.

## Implementation scope

Implement SQL migrations and create/get operations within the transfer feature. Do not implement dispatch, provider HTTP calls, recovery workers, or funds mutations yet.

- Persist the transaction identity, merchant and merchant reference, integer rupiah amount, destination bank/account, funding source from merchant configuration, bank integration account, provider reference, status, and required timestamps. Persist the integration account so configuration changes cannot alter the reference scope during retries.
- Enforce `UNIQUE(merchant_id, reference_id)`, `UNIQUE(provider_account_id, provider_reference)`, a positive amount, and a status constraint allowing only `PENDING`, `PROCESSING`, `SUCCESS`, and `FAILED`. New transactions always start as `PENDING`.
- The transaction and provider reference must be durable before contacting the bank in stage 3. `PROCESSING` will include uncertain outcomes. Timeouts and HTTP 504/500 responses must not automatically cause failure, and HTTP 200 must be interpreted together with the business response body. Final statuses require final evidence.
- Obtain `merchant_id` from the authenticated context. The request body must not select the merchant or funding source. Scope get operations and duplicate lookups to the owning merchant. Do not expose another merchant's transactions.
- The MVP supports only IDR, with positive integer amounts that fit within `int64`, and one funding source per merchant. Reject fractional amounts and overflow. Preserve leading zeros in account numbers. Normalize only as specified by the contract. Compare stored `amount`, `bank_code`, and `account_number` without floating-point arithmetic.

## Idempotency algorithm

Use explicit SQL rather than check-then-insert:

```sql
INSERT INTO transfers (...)
VALUES (...)
ON CONFLICT (merchant_id, reference_id) DO NOTHING
RETURNING id;
```

If no row is returned, retrieve the existing transaction with a separate `SELECT` using `(merchant_id, reference_id)`. Use `READ COMMITTED` semantics so the next statement obtains a fresh snapshot after the conflicting insert resolves. Do not replace this with a single-snapshot insert/select CTE. Errors from other constraints must not be swallowed as merchant-key duplicates.

- Same payload: return the existing transaction with the same ID, reference, and status, without overwriting data or introducing new side effects, including when the status is final.
- Different payload: return `409` for every status, including `SUCCESS` and `FAILED`. Preserve the original payload.
- If the design allows the existing-row lookup to return no row, define and test bounded handling. Do not loop indefinitely or use a provider call as a fallback.
- Local uniqueness constraints do not replace provider idempotency. Future retries and inquiries must reuse the existing integration account and provider reference. Do not replace the reference when handling duplicates.

## Acceptance criteria

- [ ] Migrations can be applied to an empty PostgreSQL database and rerun through the migration mechanism without duplicating schema or data.
- [ ] Create persists one `PENDING` transaction with its provider reference. Get is restricted to the owning merchant.
- [ ] Identical duplicates return the existing transaction in every status. Payload changes always return `409`.
- [ ] Different merchants may use the same merchant reference. Provider reference constraints follow the integration account scope.
- [ ] Concurrent requests produce one row per merchant/key without overwriting the existing payload or status.
- [ ] SQL, handlers, and tests remain close to the owning feature. No requests are sent to the bank yet.

## Tests and checks

Use real PostgreSQL for migrations, constraints, and concurrency tests. Synchronize goroutines with barriers/channels and independent database connections. Do not use `time.Sleep` to wait for results.

Test create/get, authentication and merchant isolation, invalid input, overflow, fractional amounts, account numbers with leading zeros, identical duplicates, changes to each compared field, duplicates in every status, and constraints for both reference scopes. Use database fixtures for final statuses rather than fake finalization endpoints.

Test concurrent identical duplicates: one row and the same ID. For concurrent requests with different payloads, verify that one wins and the other receives `409`, without overwriting data.

Run formatting, build, `go vet`, relevant unit/integration tests, and the applicable race detector checks. Record commands, exit statuses, and actual results. If PostgreSQL is unavailable, report that integration tests were not run. Database mocks do not prove constraint behavior.

## Review checklist and stopping point

- [ ] Include the list of changed files/migrations, the rationale for separate statements, and any contract changes.
- [ ] Provide clear duplicate/conflict/concurrency evidence, provider/funds limitations, and reproduction commands.
- [ ] Stop for review. Proceed to `transfer-03-provider` only after user review.