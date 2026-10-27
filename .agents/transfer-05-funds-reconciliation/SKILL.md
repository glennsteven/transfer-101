---
name: transfer-05-funds-reconciliation
description: "Stage 5 of transfer-101: atomic funds reservation/finalization and idempotent mock bank report reconciliation. Use after inquiry and crash recovery have been reviewed."
---

# Transfer 05: Funds and Reconciliation

## Procedure and stage boundaries

- Inspect the actual repository, applicable `AGENTS.md` instructions, local skills, contracts, migrations, workers, tests, and user changes. Do not assume previous stages are available based on conversation history.
- The agent implements; the user reviews the design, code, and test results. Determine the scope, acceptance criteria, and required tests before editing. Do not ask the user to write code or answer quizzes.
- Prerequisite: stages 1 through 4 are available and have been reviewed. Otherwise, report the gaps and stop without automatically implementing another stage. Adjustments to create/dispatch/recovery for funds handling are within this stage's scope and must be explained.
- Implement this stage only. Creating or reviewing a skill does not authorize running the application. Use Go 1.26.0 or later, PostgreSQL, LoB, the standard library, and a PostgreSQL driver/additional dependencies only as needed.
- Avoid ORMs, HTTP frameworks, generic repositories, DI containers, excessive layers, abstractions without concrete needs, Redis, message brokers, Kubernetes, and integrations with real banks.
- Run relevant checks and report actual results, including failures and checks that were not run, with reasons. Do not claim production readiness or fabricate benchmarks.
- Provide changed files, design rationale, limitations, and a review checklist. Stop before the next stage for review. Do not automatically publish or push.

## Funds scope

The MVP supports only IDR represented as integer rupiah amounts, with one configured funding source per merchant. Explicitly define local opening-balance/top-up fixtures. Do not add multiple currencies, fees, FX, or a general-purpose accounting framework.

- Choose an auditable funds movement model with constraints and balance invariants. For example, use available/reserved balances: reservation moves the amount into reserved funds, success consumes the reservation, and failure releases it back to available funds. Prevent negative balances and overflow.
- Creating a new transfer, reserving funds, persisting the transaction/provider reference, and scheduling dispatch must be atomic. Insufficient funds must reject creation without leaving partial rows, jobs, or reservations, according to the updated contract.
- Preserve `UNIQUE(merchant_id, reference_id)` and `INSERT ... ON CONFLICT (merchant_id, reference_id) DO NOTHING RETURNING id`. If no row is returned, retrieve the existing transaction with a separate `SELECT`. Compare existing duplicates before reserving funds: identical payloads must not reserve again; different payloads must always return `409`, regardless of the current status or balance. Compare amount, bank code, and account number.
- Reserve funds using short locks or atomic conditional updates in PostgreSQL. Concurrent distinct transfers must not overspend. Define a consistent lock acquisition order.
- Obtain `merchant_id` from the authenticated context. Persist the funding source identity and integration account on the transaction. Configuration changes must not redirect finalization of existing transactions. Apply merchant isolation to every balance query.
- Dispatch only transfers backed by valid reservations. Define an explicit migration/reset policy for demo data from earlier stages. Do not dispatch without backing funds or silently delete user data.
- Timeouts, HTTP 504/500, HTTP 200 pending, temporary inquiry not-found results, and exhausted retries must preserve the reservation and nonfinal state. `PROCESSING` includes uncertain outcomes. Requiring review is not permission to release funds.
- `SUCCESS` supported by final evidence consumes the reservation exactly once; `FAILED` supported by final evidence releases it exactly once. Status, evidence, funds movements, and recovery jobs must be updated atomically. Constraints/guards must prevent both capture and release for the same transfer.
- Dispatch, inquiry, and reconciliation must use the same concrete finalization path so races cannot duplicate funds movements. Retain only the four transfer statuses. Do not hold locks during HTTP calls or external report reads.
- Provider references remain unique within the bank integration account scope. Retries and inquiries must reuse existing references. Local uniqueness constraints do not replace provider idempotency guarantees.

## Report reconciliation scope

Reports must originate from the mock bank's durable state, not copies of application transfer tables. Define a simple format, such as CSV, containing a report ID, bank integration account, period/cutoff, reference, amount, currency, and business status. Document destination fields when available.

- Inquiry checks one operation at a particular time; reconciliation compares bank reports for a defined period against local records. Document the completeness limits of each.
- Persist report identity/hash and an import/findings audit trail to support safe reimports. Define row identity, identical/conflicting duplicates, and handling of the same report ID with changed contents.
- Match `(provider_account_id, provider_reference)`, amount, currency, and other contract-defined fields. Do not match references across accounts.
- Distinguish matched records, bank-only records, local-only/missing records, payload/amount mismatches, duplicate/conflicting rows, invalid rows, and status conflicts. Unknown statuses must not be interpreted as success.
- A missing row does not prove failure. Check cutoff/completeness and preserve the status and funds until final evidence is available. Mismatches or final-status conflicts become findings requiring review.
- Reports may finalize nonfinal transactions only when the contract defines the row as final evidence and all important fields match. Use the same atomic, idempotent finalization path as inquiry. Do not overwrite contradictory final outcomes or automatically adjust balances because of mismatches.
- Define handling for partial/corrupted files, import retries, and report access by merchant/account. Do not claim reconciliation is complete while findings remain open.

## Acceptance criteria

- [ ] Available/reserved balances remain nonnegative and consistent with opening balances and final outcomes.
- [ ] Duplicates do not reserve funds twice; concurrent transfers cannot overspend.
- [ ] Ambiguous outcomes retain reservations. Final capture/release occurs exactly once, including worker/import races.
- [ ] Reimports and import restarts do not duplicate effects. Reports originate from the durable mock bank.
- [ ] Missing records, mismatches, unknown references, and final-status conflicts remain visible without incorrect funds adjustments or false finality.

## Tests and checks

Use real PostgreSQL for constraints, rollback, balances, and concurrency. Test sufficient/insufficient funds, duplicates after balance changes, key conflicts in every status, merchant isolation, overflow, parallel creation with limited funds, ambiguous timeouts, repeated finalization, and inquiry/reconciliation races involving matching and contradictory evidence.

Test crashes at reservation/job and finalization commit boundaries. Rollbacks must not leave partial effects. Test reimports, interrupted/restarted imports, duplicate rows, references across accounts, bank-only/local-only records, amount/currency mismatches, unknown statuses, partial reports, and valid final evidence.

Use controlled barriers/channels/hooks/clocks rather than `time.Sleep` to wait for results. Run formatting, build, `go vet`, relevant tests, and the race detector. Record commands, exit statuses, actual results, and checks that were not run.

## Review checklist and stopping point

- [ ] Explain changed files/migrations, funds contract changes, and handling of existing demo data.
- [ ] Provide balance invariants, concurrency/finalization/reconciliation evidence, and open findings.
- [ ] Clearly state limitations of the funds/report simulation.
- [ ] Stop for review. Proceed to `transfer-06-verification` only after user review.