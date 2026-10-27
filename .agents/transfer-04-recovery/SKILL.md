---
name: transfer-04-recovery
description: "Stage 4 of transfer-101: inquiry, durable PostgreSQL retries, worker leases, and crash recovery without duplicate bank operations. Use after the mock bank and dispatch have been reviewed."
---

# Transfer 04: Recovery

## Procedure and stage boundaries

- Inspect the actual repository, applicable `AGENTS.md` instructions, local skills, provider contracts, schema, tests, and user changes. Do not assume previous stages are complete based on conversation history.
- The agent implements; the user reviews the design, code, and test results. Determine the scope, acceptance criteria, and required tests before editing. Do not ask the user to write code or answer quizzes.
- Prerequisite: stages 1 through 3 are available and have been reviewed. Otherwise, report the gaps and stop. Do not automatically implement another stage. Explain any supporting changes included in scope.
- Implement this stage only. Creating or reviewing a skill does not authorize running the application. Use Go 1.26.0 or later, PostgreSQL, LoB, the standard library, and a PostgreSQL driver/additional dependencies only as needed.
- Avoid ORMs, HTTP frameworks, generic repositories, DI containers, excessive layers, abstractions without concrete needs, Redis, message brokers, Kubernetes, and integrations with real banks.
- Run relevant checks and report actual results, including failures and checks that were not run, with reasons. Do not claim production readiness or fabricate benchmarks.
- Provide changed files, design rationale, limitations, and a review checklist. Stop before the next stage for review. Do not automatically publish or push.

## Implementation scope

Build a worker and durable scheduling in PostgreSQL. Store job metadata on transfers or use a dedicated job table if needed. Do not introduce a message broker. Inquiry checks one bank operation; report reconciliation and funds handling belong to stage 5.

## Durable jobs and leases

- Persist the job type, due time, attempt count, last error, lease owner/token, lease expiry, and review-required indicator as justified by actual needs. Persist transfers and their scheduling atomically. Likewise, update outcomes and subsequent jobs atomically so crashes cannot lose work.
- Claim jobs using short transactions that support multiple workers safely, such as `FOR UPDATE SKIP LOCKED` followed by a lease update. Commit before HTTP calls. Do not hold database locks while waiting for the network.
- Use a token/generation for conditional updates. A stale worker must not overwrite outcomes, reschedule jobs, or release a lease owned by a newer worker. Use the database clock for durable leases and explicit time controls in tests.
- Lease expiry does not cancel requests already accepted by the bank; attempts may overlap. Side-effect safety depends on bank idempotency with the same reference, not merely a local lease. Do not claim exactly-once delivery.
- After a restart, workers reclaim jobs once their leases expire. Shutdown must stop new claims and bound the wait for active work. Jobs must not depend on process memory.

## Inquiry and retry

- Every attempt must use the existing bank integration account and `provider_reference`. Do not replace the reference to work around timeouts or conflicts. Local uniqueness constraints do not establish the bank's state.
- Follow ambiguous submission outcomes with inquiry according to the provider contract. HTTP 200 must be interpreted together with the business response body. Timeouts, HTTP 504/500, and malformed bodies remain nonfinal. `PROCESSING` includes uncertain external outcomes.
- `Not found` is not general permission to resubmit. Resubmit only if the contract guarantees durable deduplication within the relevant scope and retention period, or definitive evidence establishes that the previous operation will not execute. Document and test the mock bank's guarantees.
- If resubmission safety remains uncertain, perform bounded inquiry attempts or mark the transaction as requiring review. Do not turn uncertainty into `FAILED`.
- Persist backoff schedules and attempt limits durably. Make the RNG controllable if jitter is used. Separate per-attempt timeouts from the overall retry budget.
- Exhausting retries must preserve a nonfinal status and a review-required indicator with a reason. Do not add transfer statuses beyond `PENDING`, `PROCESSING`, `SUCCESS`, and `FAILED`. Final statuses still require evidence.
- Final updates must be idempotent and conditional. Do not regress final statuses or overwrite contradictory evidence; record conflicts. Exhausting the retry budget must not remove the transaction from follow-up tracking.
- Preserve merchant isolation, integer IDR amounts, and the persisted funding source/integration account according to earlier-stage contracts.

## Acceptance criteria

- [ ] Pending/processing jobs can be discovered after a restart without relying on an in-memory enqueue.
- [ ] Multiple workers operate safely; expired leases can be reclaimed, and updates using stale tokens are rejected.
- [ ] Retries and inquiries reuse the same account/reference. Durable bank operations remain limited to one per key.
- [ ] A timeout after bank success is recovered through valid final inquiry evidence.
- [ ] Temporary not-found results, pending outcomes, repeated errors, and exhausted budgets do not cause false `FAILED` statuses.
- [ ] Jobs are persisted atomically, and no database locks span HTTP calls.

## Tests and checks

Use real PostgreSQL and the durable mock bank. Use barriers/channels, commit hooks, and controlled clocks/lease deadlines. Do not use `time.Sleep` to wait for results. Real-time deadlines may only serve as safeguards against hanging tests.

Test crashes at these boundaries:

- After the transfer/job commit, before claiming.
- After the claim commit, before the HTTP call.
- After the bank commit, before the response.
- After receiving a final response, before the local update.
- After committing the final status/job update.

Include worker processes that are actually terminated and restarted at controlled points, rather than relying solely on unit tests or graceful shutdown.

Test two workers, lease expiry while an older request remains active, stale worker outcomes after reclaiming, persisted backoff after restart, temporary not-found results followed by discovery, exhausted budgets/review-required handling, and one bank operation despite multiple HTTP attempts. Prove that jobs are not lost and final statuses do not regress.

Run formatting, build, `go vet`, relevant unit/integration/process tests, and the race detector. Record commands, exit statuses, actual results, and environment limitations.

## Review checklist and stopping point

- [ ] Explain changed files/migrations, lease/retry/reference rules, and final evidence.
- [ ] Provide evidence for every crash boundary, stale-token handling, and instructions for finding transactions requiring review.
- [ ] Clearly state the limitations of mock guarantees and retention.
- [ ] Stop for review. Proceed to `transfer-05-funds-reconciliation` only after user review.