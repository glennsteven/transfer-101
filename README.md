# transfer-101

A Go and PostgreSQL transfer backend simulation for handling cases where **the bank has processed a transfer, but the application receives HTTP 504**. The transfer identity is persisted before contacting the provider. Recovery seeks final evidence using the same reference while funds remain reserved.

All banks, accounts, credentials, and balances are local fixtures. Balances are not real money. Guides: [demo](docs/demo.md), [diagrams](docs/diagrams.md), [design](docs/design.md), [verification evidence](docs/verification.md), [article draft](docs/article.md), [presentation material](docs/presentation.md).

A code review on 2026-10-27 identified gaps in recording late final evidence, report snapshot completeness, and business dependency boundaries. These findings remain open. Read [open review findings](#open-review-findings) and [tests and verification](#tests-and-verification) before assessing the implementation's guarantees.

## Available features

- Transfer creation/retrieval with demo bearer authentication and merchant isolation.
- Merchant idempotency: identical keys/payloads return the existing ID; different payloads return 409 in every status.
- Durable mock bank with provider idempotency, inquiry, and CSV report snapshots.
- Scenarios for success, final rejection, ACCEPTED, success before 504/timeout, and ACCEPTED before 500.
- Manual dispatch or polling workers; short claims, leases/tokens, durable retries, bounded exponential backoff with jitter, and manual review.
- Atomic funds reservation on creation; capture/release once based on final evidence, with durable movement records.
- Verified report reconciliation with mismatch/missing/duplicate findings; reimports do not duplicate balance effects.
- Unit/integration/process tests, a PostgreSQL CI workflow, structured logging, and a bounded local load runner.

## Stack and Locality of Behavior

The module requires **Go 1.26.0** or later. mise/CI pin **Go 1.26.10** and dbmate **2.28.0**; Compose uses **PostgreSQL 17.11**. The application's only module dependency is `github.com/lib/pq v1.12.3`, a `database/sql` driver. HTTP, JSON, CSV, context, logging, random IDs, and tests use the standard library. dbmate is a separate development CLI.

```text
cmd/
  api/                  HTTP API composition and lifecycle
  mockbank/             bank process with its own database
  worker/               dispatch/inquiry recovery process
  demo-provider/        HTTP demo CLI and duplicate checks
  funds/                initialize/read simulated balances
  reconcile/            export/import/show reports and audit records
  loadtest/             bounded HTTP traffic and invariant audits
internal/
  transfer/             HTTP, auth, SQL, provider, recovery, funds, reconciliation
    migrations/         application SQL; tests stay close to their feature
  mockbank/             submission/inquiry/reports, SQL, bank tests
    migrations/         bank SQL
  health/               liveness/readiness and tests
  testdb/               test database guards and migration fixtures
config/                 example merchant/provider credentials
scripts/                container test builders/runners and demo wrappers
docs/                   contracts, design, demos, verification, article
.agents/                seven staged implementation skills
.github/workflows/      PostgreSQL verification workflow
AGENTS.md               agent instructions to read the architecture rules
ARCHITECTURE_RULES.md   LoB rules, dependency boundaries, and review workflow
```

Locality of Behavior keeps SQL, handlers, recovery, and balance rules within the `transfer` feature so status changes can be reviewed alongside their funds effects. The mock bank owns its feature and storage. `cmd` wires concrete components together. There is no ORM, HTTP framework, generic repository, DI container, or call-forwarding layer. PostgreSQL stores both state and durable jobs; Redis, message brokers, and Kubernetes are unnecessary.

[AGENTS.md](AGENTS.md) directs implementation and review to [ARCHITECTURE_RULES.md](ARCHITECTURE_RULES.md). Those rules define a flow from handlers/workers to services, then to storage/provider contracts. The current implementation still combines business decisions with SQL and uses a concrete provider in recovery. Grouping code in one package does not yet satisfy those dependency boundaries. Improvements are made incrementally within the relevant workflows while preserving API contracts, schema, and transaction invariants.

## Running locally

Prerequisites: mise, a running Docker Engine/Desktop with Compose v2 supporting `--wait`, and access to tool/module/image downloads for the first run. Repository: [glennsteven/transfer-101](https://github.com/glennsteven/transfer-101). Clone through SSH, then run setup from the project root:

```sh
git clone git@github.com:glennsteven/transfer-101.git
cd transfer-101
```

```sh
mise trust
mise install
mise run setup
mise run funds:init merchant_a 10000000
mise run funds:show merchant_a
mise run dev:504
```

`setup` creates `.env` and `.env.mockbank` only if they do not already exist, starts both PostgreSQL instances, applies dbmate migrations for both features, and builds the binaries. `funds:init` explicitly records an opening balance of IDR 10,000,000. Repeating it for an existing account accepts only the same opening amount. For an account with a different opening balance, inspect `funds:show` and use its allocated balance. This command is not a top-up.

`dev:504` starts the API, the bank in the `success_504` scenario, and the worker. In a second terminal:

```sh
mise run demo
mise run funds:show merchant_a
```

Expected result: `app=SUCCESS bank=SUCCEEDED`, duplicate identities remain unchanged, and `recovered=SUCCESS`. The initial status may already be final because the worker runs concurrently. The [separate demo](docs/demo.md#bank-sukses-respons-504-lalu-inquiry) shows PROCESSING/held before the worker starts.

`mise run dev` uses the scenario from `.env.mockbank` (default: success). Ctrl+C stops foreground processes. `mise run db:stop` stops both databases without deleting volumes. Stop existing writers before migrations. Do not delete volumes as a restart or test-cleanup mechanism.

### Configuration and public credentials

mise loads `.env.example`, `.env.mockbank.example`, `.env`, then `.env.mockbank`; local files override examples. Restart processes after changing configuration.

| Component | Local default |
| --- | --- |
| API | `http://127.0.0.1:8080` (`HTTP_ADDR`) |
| Bank | `http://127.0.0.1:8081` (`MOCK_BANK_HTTP_ADDR` / `MOCK_BANK_URL`) |
| Application PostgreSQL | `127.0.0.1:54329`, database/user/password `transfer_demo` |
| Bank PostgreSQL | `127.0.0.1:54331`, database/user/password `mock_bank` |
| Test PostgreSQL | `127.0.0.1:54330`, database/user/password `transfer_test`, tmpfs |
| Merchant A | Token `demo-merchant-a-token`, merchant `merchant_a`, funds `funds_a` |
| Merchant B | Token `demo-merchant-b-token`, merchant `merchant_b`, funds `funds_b` |
| Integration account for both merchants | `bank_account_demo_a`, bank token `demo-bank-account-a-token` |

Credentials are intentionally public and limited to loopback demos. Merchant B needs an opening balance before creating transfers. Merchant identity comes from the authenticated context. The body cannot select a merchant, funding source, or provider account. Mappings: [merchants](config/merchants.example.json), [providers](config/provider-accounts.example.json).

| Option | Default/purpose |
| --- | --- |
| `DATABASE_URL`, `MOCK_BANK_DATABASE_URL` | TCP PostgreSQL URLs from the example env files; quote the entire shell assignment because of `?`/`&`. |
| `MERCHANT_CREDENTIALS_FILE`, `PROVIDER_CREDENTIALS_FILE` | Example credential files in config, read at startup. |
| `DB_TIMEOUT`, `PROVIDER_TIMEOUT`, `SHUTDOWN_TIMEOUT` | `2s`, `2s`, `5s`. |
| `RECOVERY_LEASE`, `RECOVERY_POLL` | `30s`, `500ms`. |
| `RECOVERY_BASE_BACKOFF`, `RECOVERY_MAX_BACKOFF` | `1s`, `1m`; due times are persisted in the database. |
| `MOCK_BANK_SCENARIO` | `success`; affects new operations only. |
| `MOCK_BANK_RESPONSE_DELAY` | `5s` for timeout scenarios. |
| `MOCK_BANK_INQUIRY_NOT_FOUND` | `false`; simulates visibility delays without deleting operations. |

### Migrations and existing data

```sh
mise run migrate
mise run db:status
```

SQL lives in feature directories with dbmate up/down migrations, `CREATE TABLE IF NOT EXISTS`, and no explicit `COLLATE`. Metadata uses `dbmate_schema_migrations`; legacy runner metadata is retained. `IF NOT EXISTS` does not repair schema drift or replace existing constraints. Run migrations from one process. The API does not execute DDL automatically.

The funds migration preserves historical transfers as `funds_state=LEGACY`. Older PENDING/PROCESSING transfers receive `LEGACY_FUNDS_UNACCOUNTED`, and their schedules are stopped. Historical balances or movements are not inferred; new opening balances do not adopt them. Resolution requires a manual, evidence-based decision. See the [legacy policy](docs/funds-reconciliation.md). Rollbacks may delete tables/data belonging to the reverted stage and are not part of restarting.

## API and statuses

With the API running and sufficient funds:

```sh
curl --max-time 10 -i http://127.0.0.1:8080/transfers \
  -H 'Authorization: Bearer demo-merchant-a-token' \
  -H 'Content-Type: application/json' \
  -d '{"reference_id":"readme-order-001","amount":150000,"bank_code":"001","account_number":"00123456789"}'
```

A new request returns `201 Created` with `Location: /transfers/{id}`. Example body before the worker runs (illustrative ID/timestamps):

```json
{
  "id": "trf_example",
  "reference_id": "readme-order-001",
  "provider_reference": "prv_example",
  "amount": 150000,
  "currency": "IDR",
  "bank_code": "001",
  "account_number": "00123456789",
  "status": "PENDING",
  "created_at": "2026-10-23T00:00:00Z",
  "updated_at": "2026-10-23T00:00:00Z",
  "funds_state": "HELD"
}
```

An active worker may advance the status before the response is read. `201` means the resource was persisted, not that the bank succeeded. Amounts are always integer IDR rupiah using `int64`/`BIGINT`, without floating point. Bank codes and account numbers are strings with leading zeros preserved; `001` is a fixture code.

| Status | Meaning | Funds for new transfers |
| --- | --- | --- |
| PENDING | Persisted, awaiting dispatch. | Already reserved as held funds. |
| PROCESSING | Dispatch has started, including uncertain external outcomes. | Remain held. |
| SUCCESS | Final SUCCEEDED evidence with matching identity/payload. | Held funds consumed once, CAPTURED. |
| FAILED | Final REJECTED evidence with a valid reason. | Held funds returned once, RELEASED. |

`needs_review`/`review_reason` are metadata, not a fifth status. Timeouts/504/500, HTTP 200 ACCEPTED, not-found results, and exhausted retries do not prove final failure. Budget exhaustion preserves PROCESSING and held funds.

| Endpoint/behavior | Result |
| --- | --- |
| New POST /transfers | 201; atomic creation/reference/job/reservation without a bank HTTP call. |
| Same key + payload | 200, existing ID/reference, without another reservation. |
| Same key; different amount/bank_code/account_number | 409 PAYLOAD_CONFLICT in every status. |
| Insufficient/uninitialized balance | 422 INSUFFICIENT_FUNDS; rollback without partial changes. |
| GET /transfers/{id} | 200 for the owner; 404 if absent or owned by another merchant. |
| POST /transfers/{id}/dispatch | Empty body; synchronous PENDING dispatch with worker claim/fencing. |
| GET /livez, GET /readyz | Liveness 200; database readiness 200 or 503. |

Use the actual creation ID for GET/dispatch. Example error: `{"error":{"code":"PAYLOAD_CONFLICT","message":"reference_id already used with a different payload"}}`. The [API contract](docs/api-contract.md) details the 4 KiB body limit, validation, and other responses.

## Idempotency, recovery, and funds

Creation uses `INSERT ... ON CONFLICT (merchant_id, reference_id) DO NOTHING RETURNING id`, followed by a separate SELECT statement if the insert loses the conflict. Both execute within a READ COMMITTED transaction; SELECT obtains a fresh snapshot. Only a new insert performs the conditional `available >= amount` reservation and RESERVE movement. The commit persists the reference/job/funds before contacting the provider.

The worker claims due jobs using `FOR UPDATE SKIP LOCKED`, persists a token/expiry/attempt, and commits. HTTP runs after locks are released. Outcome persistence checks the token and active lease; stale workers cannot overwrite claims/final statuses. Final status, CAPTURE/RELEASE, and evidence/audit records commit atomically through settlement shared by dispatch, inquiry, and reconciliation.

Every attempt starts with inquiry, including before the first submission. NOT_FOUND may lead to submission with the existing reference/payload only when the mock contract guarantees the account, dataset epoch, and resubmission window. The default window is 15 minutes after creation; the default budget is 8 claims, including claims interrupted by crashes. Exponential backoff is bounded, with jitter ranging from half the cap to the cap. Schedules/attempts survive restarts. See [diagrams](docs/diagrams.md), [recovery](docs/recovery.md), and the [provider contract](docs/provider-contract.md).

Inquiry reads one operation. Reconciliation compares the bank's CSV snapshot against local records. Only verified/complete reports with matching scope/epoch/payload may finalize nonfinal transactions. Missing/mismatched records do not prove failure; conflicts with final statuses become review findings. Identical imports reuse existing audit records without a second balance effect. See the [reconciliation demo](docs/demo.md#rekonsiliasi-laporan).

## Open review findings

A source review on 2026-10-27 identified the following. No implementation changes or PostgreSQL regression tests have yet demonstrated fixes.

- Report finalization clears the lease and closes the active attempt. If a contradictory worker response arrives afterward, `finish` returns `errLeaseLost` before comparing final evidence. Status and funds are not overwritten, but the conflict is not persisted as a durable review finding. This path needs to record late evidence without granting stale workers permission to change status/funds. See [recovery](internal/transfer/recovery.go) and [report finalization](internal/transfer/reconciliation.go).
- The mock bank establishes a `REPEATABLE READ` snapshot before obtaining the cutoff timestamp, then labels the metadata `complete=true`. Operations committed between those points may be invisible despite having timestamps before the cutoff. The immutable report can therefore produce misleading missing-record findings. Cutoff/completeness semantics must be aligned with the snapshot and tested with concurrent commits. See [report generation](internal/mockbank/reports.go).
- Create/recovery workflows still depend directly on `*sql.DB`, and recovery uses concrete `*ProviderClient`. The separation into services, storage/provider contracts, and adapters required by the architecture rules has not been implemented. Fixes must preserve atomic transactions and add workflow tests through small contracts. See [creation](internal/transfer/store.go), [recovery](internal/transfer/recovery.go), and the [dependency rules](ARCHITECTURE_RULES.md#3-dependency-boundaries).

## Tests and verification

```sh
mise run test
mise run test:e2e
mise run test:integration
mise run test:race
```

`mise run test` runs unit tests with the race detector, without PostgreSQL. The `test:e2e`, `test:integration`, and `test:race` tasks prepare a separate test PostgreSQL instance, binaries matching the container architecture, and dbmate migrations in random fixture schemas. A guard checks the `transfer_test` database/role before DDL; cleanup affects fixture schemas only. Without the integration tag, `go test ./...` does not run PostgreSQL constraint/concurrency tests.

Native alternative (dbmate on PATH):

```sh
docker compose --profile test up -d --wait postgres-test
env 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=3' \
  go test -race -tags=integration -count=1 -timeout=10m ./...
```

[CI](.github/workflows/verification.yml) runs formatting/vet/build/unit/integration/focused `-count=20`/race checks with its own PostgreSQL service. Crash tests use child-process SIGKILL and barriers at commit boundaries. Asynchronous tests use controlled synchronization without `time.Sleep` to wait for results.

Latest checks, dated 2026-10-27, used Go `1.26.10 darwin/arm64`:

| Check | Result |
| --- | --- |
| `go build ./...` | Passed. |
| `go vet ./...` | Passed. |
| `gofmt -l cmd internal` | Passed; no files listed. |
| `go test -count=1 -timeout=60s ./...` | Passed for the suite without the integration tag. |
| `go test -race -count=1 -timeout=60s ./...` | Passed for the suite without the integration tag. |
| `go test -tags=integration -run '^$' ./...` | Integration suite compiled successfully; no tests executed. |
| `TestPostgresAPIStartupAndRoutes` with the test database at `127.0.0.1:54330` | Failed before fixtures/test flow ran: `connect: operation not permitted`. |
| Docker access through `docker compose --profile test ps` | Denied by workspace Docker socket permissions. |

PostgreSQL constraints/concurrency, crash recovery, integration race tests, and load tests were not reverified in the latest session. The findings above come from code inspection; passing unit tests do not prove those paths correct. Remote CI results have not been observed.

Historical stage 6 notes in [docs/verification.md](docs/verification.md), dated 2026-10-23, report passing formatting/vet/unit/unit-race checks on Go 1.26.2 darwin/arm64, plus five integration/process suites and focused `-count=20` runs on PostgreSQL 17.11 Linux arm64. WAL initially filled the 256 MiB test tmpfs; repeated runs passed after the harness increased it to 1 GiB. These historical results do not reverify the latest code or prove the review findings have been addressed.

The load runner requires active demo processes, binaries from setup/build, and sufficient funds:

```sh
mise run loadtest -- --requests 200 --concurrency 16 --duration 20s --http-timeout 3s --completion-timeout 30s
```

The runner distinguishes API latency from observed finality, then checks local/provider uniqueness and funds. The stage 6 run did not pass full correctness checks: a balance audit query failed and was fixed, but the rerun was blocked by Docker permissions. No validated benchmark is claimed. Load data is retained, and simulated funds are consumed. See the [load guide](docs/load-test.md), [complete actual results](docs/verification.md), and [stage 7 documentation validation](docs/verification-publish.md).

JSON logs contain transaction_id, provider_reference, operation, attempt, duration, and outcome without tokens/full account numbers. Durable balance/reconciliation audits live in PostgreSQL. The [demo](docs/demo.md) provides read-only queries for transactions, effective bank transfers, and balances without cleaning up demo data.

## Trade-offs and limitations

- Retry safety depends on provider deduplication/retention within the account/reference scope. Local constraints do not replace this; there is no cross-system exactly-once claim.
- PostgreSQL polling and sequential workers per process keep the model simple. Leases without heartbeats require consistent timeout/lease durations; no capacity study has been conducted.
- Held funds reduce available balances while outcomes remain uncertain. Manual review has no UI or general-purpose resolution command yet.
- One configured funding source per merchant, IDR only, without fees/top-ups/FX. Simple movement records are not a general ledger.
- Mock ACCEPTED does not automatically become final. Full-snapshot reports are limited to 1,000 rows/8 MiB and are not real daily bank statements. Resetting history without rotating the epoch violates the contract.
- Static bearer tokens and loopback HTTP, without TLS/online rotation/HA/real banks. Local tests do not establish production readiness.

The article and presentation material are prepared locally for review. Pushes, deployments, and publication are not performed automatically.
