---
name: transfer-07-publish
description: "Stage 7 of transfer-101: prepare the README, reproducible demos, design decisions, and a Medium article draft based on actual evidence. Use after verification has been reviewed; do not automatically publish or push."
---

# Transfer 07: Publish Preparation

## Procedure and stage boundaries

- Inspect the actual repository, applicable `AGENTS.md` instructions, local skills, implementation, contracts, tests, run results, and user changes. Do not assume previously discussed features or measurements are available.
- The agent writes and prepares deliverables; the user reviews the design, code, evidence, and drafts. Determine the scope, acceptance criteria, and required checks before editing. Do not ask the user to write code or answer quizzes.
- Prerequisite: stages 1 through 6 are available and have been reviewed. Otherwise, report the gaps and stop without automatically implementing another stage. Documentation/demo fixes within scope must reflect actual behavior.
- Work on this stage only. Creating or reviewing a skill does not authorize running the application. Retain Go 1.26.0 or later, PostgreSQL, LoB, the standard library/dependencies as needed, and the mock bank.
- Avoid ORMs, HTTP frameworks, generic repositories, DI containers, excessive layers, abstractions without concrete needs, Redis, message brokers, Kubernetes, and integrations with real banks.
- Run relevant checks and report actual results, including failures and checks that were not run, with reasons. Do not claim production readiness or fabricate benchmarks, real incidents, or bank guarantees.
- Provide changed files, design rationale, limitations, and a review checklist. Stop for review. Do not automatically publish or push. The skill name does not authorize creating a remote repository, release, deployment, or Medium submission. Prepare local artifacts only. External actions require separate user instructions.

## README scope

Explain the concrete problem: a timeout/HTTP 504 may occur after the bank has processed a transfer; retrying without idempotency risks duplicate operations. Document the actual workflow from transaction/reference persistence through claiming/submission, inquiry/recovery, funds handling, and reconciliation.

Include:

- Prerequisites: Go 1.26.0 or later, PostgreSQL, and the tooling used; demo configuration/credentials, migrations, seeds, startup/shutdown, and cleanup limited to demo data.
- Feature/LoB structure, create/get routes, demo authentication, merchant isolation, contract-aligned request/response examples, integer IDR amounts, and one configured funding source per merchant.
- The four statuses PENDING/PROCESSING/SUCCESS/FAILED and their final-evidence requirements. HTTP 200 pending and timeouts/HTTP 504/500 are not automatically final. Exhausted retry budgets still require review.
- Merchant/provider idempotency, reference scope, amount/bank_code/account_number comparisons, `409` even for final transactions, and the rationale for `INSERT ... ON CONFLICT ... DO NOTHING RETURNING` followed by a separate `SELECT`.
- Transaction/reference persistence before the call; reuse of existing accounts/references for retries and inquiries. No database locks during HTTP calls. Explain leases/stale workers and why local uniqueness constraints do not provide provider idempotency.
- Funds reservation/finalization, insufficient funds, retained reservations for ambiguous outcomes, and inquiry versus report reconciliation. Not-found results and missing report rows do not automatically prove failure.
- Test/CI/load commands, actual results, diagnostics/review-required handling, mock guarantee limitations, and future work.

Do not create passing CI badges, repository URLs, result screenshots, or performance figures without real sources. If no remote repository exists, state that the artifacts are ready for local review. Do not invent URLs.

## Reproducible demo scope

Provide commands/scripts and fixtures with expected observations matching the implementation, rather than hardcoded result IDs/timestamps:

1. Bootstrap PostgreSQL, migrations, demo merchants/credentials, and opening balances.
2. Demonstrate normal creation, identical duplicates returning the same ID, and payload conflicts returning `409`.
3. Demonstrate concurrent duplicates and transfers with limited funds. Prove one operation per key and no overspending.
4. Demonstrate bank success followed by HTTP 504: the application remains `PROCESSING`, with funds reserved until final recovery evidence is available.
5. Terminate the worker at a controlled boundary and restart it. Demonstrate lease recovery, reuse of the same reference, and no duplicate bank operations.
6. Demonstrate exhausted budgets/review-required handling without false `FAILED` statuses, plus temporary inquiry not-found results according to the contract.
7. Demonstrate matched/missing/mismatched reconciliation records and idempotent reimports. Valid final evidence finalizes once, while conflicts remain visible.

Use available scenario/barrier/clock controls rather than `time.Sleep` to wait for asynchronous completion. Reset/cleanup must be explicit and restricted to the demo database/namespace, without implicitly deleting user data. Do not invent demo durations.

## Design decisions and Medium draft

Record actual trade-offs: standard library and direct SQL, LoB, PostgreSQL as durable state/queue, provider deduplication guarantees and retention, lease limitations, evidence-based finality, reserved funds, and reconciliation. Do not claim exactly-once delivery or production protections that have not been demonstrated.

Create a local Markdown draft, such as `docs/medium-draft.md`, covering the problem, reproduction of an ambiguous HTTP 504, the solution, design decisions, test/crash/concurrency evidence, limitations, and instructions for trying the demo. Label bank examples as simulations. Every measurement must reference actual configuration/run results. Do not fabricate real-bank incident narratives or benchmark figures.

## Acceptance criteria

- [ ] The README is sufficient to run the project locally from a clean environment, with explicit versions/configuration.
- [ ] API/status/reference/funds/recovery examples match the code, and local links are valid.
- [ ] Demos include commands, expected observations, data isolation, and actual run results.
- [ ] Design decisions and the article distinguish tested evidence, mock contract assumptions, and limitations.
- [ ] No real secrets, production-readiness claims, fabricated benchmarks, automatic publication, or automatic pushes are included.

## Tests and checks

Run the quickstart/demos against an explicitly identified clean demo database or namespace. Verify through APIs and invariant queries, not only screenshots/logs. Run the referenced test commands and check payloads, links, and document consistency. If the environment blocks verification, record exactly what remains unverified. Do not report it as successful.

There is no need to repeat large load tests if stage 6 evidence remains applicable and code/configuration has not changed. If changes affect results, rerun the affected checks and update the evidence/drafts.

## Review checklist and stopping point

- [ ] Include the file list, artifact organization rationale, commands, exit statuses, and actual results.
- [ ] Reviewers can trace README/article claims to code or test evidence.
- [ ] Limitations/gaps are explicit, and all artifacts remain local.
- [ ] Stop for review. There is no automatic next stage. Publish only upon subsequent user instructions.