---
name: transfer-06-verification
description: "Stage 6 of transfer-101: deterministic PostgreSQL/concurrency tests, CI, observability, and reproducible load testing. Use after funds handling and reconciliation have been reviewed."
---

# Transfer 06: Verification

## Procedure and stage boundaries

- Inspect the actual repository, applicable `AGENTS.md` instructions, local skills, contracts, tests, tooling, and user changes. Do not assume implementations or test results exist without checking.
- The agent implements and runs tests; the user reviews the design, code, and evidence. Determine the scope, acceptance criteria, and required tests before editing. Do not ask the user to write code or answer quizzes.
- Prerequisite: stages 1 through 5 are available and have been reviewed. Otherwise, report the gaps and stop without automatically executing another stage. Bug fixes supported by failing tests are within scope, provided they include rationale and regression evidence.
- Implement this stage only. Creating or reviewing a skill does not authorize running the application. Use Go 1.26.0 or later, PostgreSQL, LoB, the standard library, and a PostgreSQL driver/additional dependencies only as needed.
- Avoid ORMs, HTTP frameworks, generic repositories, DI containers, excessive layers, abstractions without concrete needs, Redis, message brokers, Kubernetes, and integrations with real banks.
- Run relevant checks and report actual results, including failures and checks that were not run, with reasons. Do not claim production readiness or fabricate benchmarks.
- Provide changed files, design rationale, limitations, and a review checklist. Stop before the next stage for review. Do not automatically publish or push. Local results do not prove that a remote workflow has run.

## Scope and test matrix

Consolidate the deterministic test harness, integration/process tests, CI, observability, and load testing. Basic tests still belong in the stages that own each feature. This stage completes cross-feature evidence rather than postponing correctness until load testing.

| Area | Required evidence |
| --- | --- |
| Merchant idempotency | Same key/payload in every status returns the same ID; changing amount/bank_code/account_number returns 409; authenticated merchants remain isolated. |
| Constraints/concurrency | Real PostgreSQL: one row for parallel duplicates; one winner for conflicting payloads; provider references are unique within the account scope; balances cannot be overspent. |
| Provider | Durable mock: one operation per key despite multiple attempts; conflicts are rejected; restarts preserve state. |
| Outcomes/statuses | PENDING/PROCESSING are nonfinal; HTTP 200 pending is not success; timeouts/504/500/malformed bodies are not proof of failure; finality requires business evidence. |
| Recovery | Commit-before-call, reuse of existing references, crash/restart, lease reclaim, stale worker rejection, ambiguous not-found results, and review-required handling after budget exhaustion. |
| Funds | Integer IDR amounts, reservation once, ambiguous outcomes retain funds, capture/release once, complete rollback, and safe finalization races. |
| Reconciliation | Distinct from inquiry; idempotent reimports/restarts; visible missing/mismatched/conflicting records; valid final evidence resolves transactions without duplicate funds movements. |

- Use channels/barriers/hooks to control event ordering and controlled clocks/timers for backoff/leases. Do not use `time.Sleep` or sleep-based polling to wait for asynchronous results. Real-time deadlines may only prevent tests from hanging.
- Isolate state for each test/run through explicit schemas/databases/fixtures. Do not reset or drop user databases. Missing PostgreSQL must not cause silent skips that are then reported as PASS. Database mocks do not prove constraint/concurrency behavior.
- Include controlled process crashes/restarts, not only unit tests or graceful shutdown. Local uniqueness constraints and leases do not replace provider idempotency.
- Run formatting, build, `go vet`, unit/integration/process tests, and `go test -race` for relevant packages. Add checks based on concrete risks and report coverage gaps.

## CI

Set up GitHub Actions with Go 1.26.0 or later and a PostgreSQL service: readiness, migrations, formatting/vet/build checks, unit/integration/concurrency tests, and relevant race detector checks. Select action/image/tool versions that are actually available at implementation time. Use fixture credentials, not real secrets.

CI must fail when prerequisites or integration tests fail rather than converting failures into skips. Heavy load tests may run as manual jobs or local procedures. Distinguish workflow inspection, local command results, and remote CI runs that were actually observed.

## Observability

- Use structured logs, such as `log/slog`: transfer/request/attempt IDs, status transitions, sources of final evidence, transport/business errors, retries, and lease details where needed. Redact credentials and account data.
- Expose pending/processing backlogs, job age, review-required items, provider attempt counts/latency, lease expirations, and reconciliation findings through simple mechanisms. Diagnostic SQL, small counters, and the standard library are sufficient. Do not add a platform without a concrete need.
- Distinguish HTTP requests, unique transfers, provider attempts, durable bank operations, final outcomes, and outstanding ambiguous transfers. Avoid transfer/reference IDs as high-cardinality metric labels.
- Logs are not a queue or the primary source of status. Durable evidence remains in PostgreSQL.

## Reproducible load testing

Create a runner/script with demo credentials/merchants, a seed/dataset, request count, concurrency, timeouts, scenario mix, and duration/operation limits. Default to the local application and mock bank only. Validate the target and namespace each run's data. Cleanup must affect only data owned by that run.

Test new requests, identical duplicates, payload conflicts, limited balances, and ambiguous HTTP 504 outcomes. After traffic ends, drain/check processing with a deadline, then query PostgreSQL invariants. Traffic completion does not mean every transfer is final. Explicitly count outstanding transfers and those requiring review.

Report the machine/OS, Go/PostgreSQL versions, commit when available, configuration, elapsed time, total requests, error breakdown, throughput, and latency distributions actually measured. Distinguish endpoint latency from transfer completion time. Prove one row/bank operation per key and balance consistency after recovery. Do not present example numbers as benchmarks or infer production capacity from a local mock.

## Acceptance criteria

- [ ] The matrix has runnable tests/evidence; failures and untested areas are visible.
- [ ] Tests use real PostgreSQL; asynchronous tests do not depend on `time.Sleep`.
- [ ] CI has explicit prerequisites; remote status is reported only when an actual run exists.
- [ ] Diagnostics can trace ambiguous outcomes without exposing credentials or account data.
- [ ] Load testing includes commands/configuration/run data, actual results, and bank/funds invariant checks.

## Review checklist and stopping point

- [ ] Include changed files and rationale for bug fixes/dependencies.
- [ ] Record commands, exit statuses, actual results, load configuration, and limitations.
- [ ] Clearly distinguish correctness evidence and local performance observations from untested targets.
- [ ] Stop for review. Proceed to `transfer-07-publish` only after user review.