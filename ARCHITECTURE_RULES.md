# Architecture Rules — Transfer-101

## 1. Purpose and scope

Use LoB (Locality of Behavior) to organize features, apply SOLID selectively to code design, and use Clean Architecture dependency boundaries to protect business logic.

Priorities: transaction correctness, traceable code, meaningful tests, and simplicity. The number of directories, interfaces, or layers is not a measure of quality.

This document guides implementation and review by developers and coding agents. It does not claim that the repository already follows every rule. Inspect the code, schema, and tests before making changes.

- Use Go 1.26 or later, following the project baseline. Do not change the version or add dependencies without a concrete need.
- Prefer the standard library. Retain the PostgreSQL driver and existing libraries where appropriate.
- Preserve API contracts, schema, and correct behavior when reorganizing code.
- Do not rewrite the entire repository merely to standardize its architecture.

## 2. Organize by feature

The following is an example target structure, not a mandatory list of files:

```text
cmd/api/main.go
cmd/worker/main.go          # only if the worker needs a separate executable
internal/transfer/
    transfer.go            # types, statuses, business rules
    service.go             # transfer submission workflow
    reconcile.go           # reconciliation workflow, when needed
    handler.go             # inbound HTTP adapter
    postgres.go            # persistence adapter
    provider.go            # provider HTTP adapter
    service_test.go
    handler_test.go
    postgres_test.go
migrations/
```

- Keep code and tests that change for the same feature close together.
- Start with one `transfer` package. Separate files by responsibility.
- Do not create global `controllers`, `services`, `repositories`, or `entities` directories that split every feature across technical layers.
- One file per struct, one interface per implementation, and a new DTO at every function boundary are unnecessary.
- Create shared packages only for actual cross-feature needs. Avoid catch-all `utils`, `common`, or `helpers` packages without a clear responsibility.
- Split packages when stronger boundaries or independent responsibilities justify it, not simply because the file count increases.

## 3. Dependency boundaries

Expected relationships:

```text
HTTP handler / worker -> service -> storage / provider contracts
Postgres adapter      -> implements storage contract
HTTP provider adapter -> implements provider contract
main                  -> constructs and connects dependencies
```

- Business logic must not accept `http.Request`, `http.ResponseWriter`, `*sql.DB`, `*sql.Tx`, or provider SDK responses as business contracts.
- Services use business types and small interfaces required by their workflows.
- Adapters translate external details: SQL, database constraints, provider JSON, HTTP status codes, and network errors.
- Handlers own transport concerns: decoding, format validation, service calls, and mapping results to HTTP responses.
- Services still validate business invariants so worker calls cannot bypass those rules.
- Workers invoke the same business workflows. Do not duplicate status transition rules inside workers.
- Wire dependencies explicitly through constructors, usually in `main`. Avoid mutable global dependencies, service locators, and DI frameworks.

**Single-package limitation:** separating files does not enforce access boundaries through the compiler. Within one package, these rules rely on design, review, and tests. Do not claim Go enforces the dependency rule merely because code lives in different files.

If boundaries are repeatedly violated, move adapters into subpackages such as `internal/transfer/postgres` and `internal/transfer/providerhttp`. Adapters may import contracts from `transfer`; `transfer` must not import its adapters. `main` imports both. Move the handler into a separate package too if HTTP isolation needs to be enforced through the import graph. Avoid import cycles, and do not introduce packages before this need arises.

## 4. Apply SOLID pragmatically

| Principle | Project rule | Avoid |
| --- | --- | --- |
| Single Responsibility | Separate business decisions, transport, and persistence according to their reasons to change. | A service that decodes JSON, executes SQL, and writes HTTP responses. |
| Open/Closed | Use replaceable dependencies at real external boundaries. Change business logic directly when business rules change. | Plugin frameworks, registries, or generic strategies for hypothetical requirements. |
| Liskov Substitution | Real implementations and fakes must honor the same contract semantics, including errors, idempotency, and cancellation. | Fakes that always succeed or adapters that silently retry transfers without safety guarantees. |
| Interface Segregation | Keep interfaces as small as their consumers need. Separate inquiry and report capabilities when they differ. | Large provider interfaces that require unsupported methods. |
| Dependency Inversion | Services depend on the contracts they need; adapters implement those contracts. | Services constructing their own HTTP clients or databases, or depending on concrete adapters. |

Not every struct needs an interface. Use concrete types for values, pure business rules, and local components that do not need substitution. Do not introduce interfaces merely to wrap methods that forward calls without adding value.

## 5. Contracts and dependency injection

Define contracts on the consumer side. This example illustrates a boundary only; adapt names and types to the existing code:

```go
type Provider interface {
    Transfer(ctx context.Context, req ProviderRequest) (ProviderResult, error)
}

type Service struct {
    provider Provider
}

func NewService(provider Provider) *Service {
    return &Service{provider: provider}
}
```

- Pass `context.Context` as the first argument to I/O operations. Do not store request contexts in structs.
- Storage method names must communicate operations and guarantees, such as atomic claiming or conditional finalization. Do not hide transaction semantics behind a generic CRUD repository.
- If a business decision requires multiple atomic database changes, express that requirement in the contract. The adapter must execute those changes in the same transaction.
- Use explicit result types when callers need to distinguish a new transfer, an identical duplicate, and a conflicting payload.
- Inject clocks or ID generators only when they materially affect tests or behavior.
- Avoid base repositories, generic services, event buses, and unit-of-work frameworks until a demonstrated need exists.

## 6. Protect transfer invariants

### Idempotency

- A request is identified by `(merchant_id, reference_id)`. Different merchants may use the same reference.
- Repeating a request with the same business payload returns the stored transfer. It must not create another outbound transfer.
- Changing any of amount, account number, or bank code under the same identity is a conflict, not a new request. Map this to HTTP `409 Conflict`.
- Compare against stored data using consistent normalization. Do not remove leading zeros from account numbers.
- Database uniqueness constraints protect against concurrent requests. A preliminary `SELECT` alone is insufficient.
- Do not recycle idempotency keys for different transfers. Define an explicit retention policy before deleting keys.
- Use a stable provider reference for each transfer. Uniqueness constraints must follow the provider contract and project schema.

### Status and uncertainty

- Preserve the persisted statuses `PENDING`, `PROCESSING`, `SUCCESS`, and `FAILED` unless a design change is explicitly agreed upon.
- `PENDING` means the transfer has not been claimed for submission. `PROCESSING` means it has been claimed and its final outcome is not yet known.
- A timeout, HTTP 504, disconnected connection, or crash after claiming does not prove failure. Preserve uncertainty and reconcile.
- A crash after claiming but before sending may be indistinguishable from a crash after the provider receives the request. Do not automatically mark it `FAILED` without trustworthy evidence.
- Use `FAILED` only for definitive failures under the provider contract, or evidence that submission did not occur and will not occur.
- `UNKNOWN` may describe the outcome to clients. It does not automatically require a new database status.
- Do not refund or resubmit merely because a local deadline expired.
- Finalization must be conditional and idempotent. Do not regress a final status or overwrite a final outcome with a delayed worker response. Escalate conflicting evidence for investigation.

### Concurrency and transactions

- Claim transfers and change their statuses atomically, using conditional updates or row locking as appropriate to the existing implementation.
- End database transactions before network calls. Do not hold row locks while waiting for providers.
- Persist the transfer identity and required references before sending an outbound request.
- Finalize using short transactions and checks for allowed current statuses.
- Do not add a ledger, balance debits, or refunds unless they are within the project scope.

### Retry and reconciliation

- Retry outbound submissions only if the provider contract guarantees deduplication with the same reference, or definitive evidence establishes that the previous submission was not processed and a retry is safe.
- A new reference does not resolve the risk of retrying a transfer with an unknown outcome.
- Use inquiry for reconciliation when available, according to the provider contract. Otherwise, use authoritative periodic reports.
- Absence from one report does not necessarily mean failure. Check the report's time coverage, completeness, and provider settlement rules.
- Report imports must tolerate duplicate files or rows and validate references and relevant transaction attributes.
- Workers must tolerate repeated and concurrent execution, with bounded retries/backoff and visibility into unresolved transfers.
- Without final evidence, preserve the uncertain status and provide an investigation path. Do not invent a final outcome.

## 7. Errors, timeouts, and resources

- Distinguish business errors, definitive provider failures, uncertain provider outcomes, and internal errors.
- Adapters translate external errors; handlers map business errors to HTTP. Do not use HTTP status codes as domain rules.
- Wrap errors with context using `%w`. Use `errors.Is` or `errors.As` when callers need to distinguish error kinds.
- Configure HTTP timeouts and I/O deadlines. Always close response bodies and limit body reads where relevant.
- Client cancellation does not cancel a transfer already accepted by the provider. Recover through persisted state and workers, rather than untracked request goroutines.
- Background workers have their own lifecycle context and controlled shutdown.
- Logs must connect transfer IDs/references, operations, and outcomes without exposing credentials or full account numbers.

## 8. Testing strategy

| Area | Test type | Required evidence |
| --- | --- | --- |
| Business rules | Unit | Amount validation, payload mismatches, and status transitions. |
| Service | Unit with small fakes | Timeouts remain unresolved; duplicates do not trigger resubmission; dependency errors are handled correctly. |
| Handler | `httptest` | Parsing, format validation, error/status mapping, and response contracts. |
| Provider HTTP | `httptest.Server` | Response mapping, timeout/cancellation, invalid bodies, and uncertain outcomes. |
| PostgreSQL | Integration with PostgreSQL | Unique constraints, atomic claiming, rollback, conditional updates, and concurrent requests. |
| Reconciliation | Unit and integration at appropriate boundaries | Final evidence, duplicate reports, incomplete reports, and repeated finalization. |

- Test behavior and invariants, not private method call sequences or internal struct layouts.
- Fakes must realistically represent the semantics under test. Mock storage does not prove row locking or atomicity.
- Use channels, barriers, or explicit synchronization for concurrency tests. Do not use `time.Sleep` to guess when a goroutine has finished. Deadlines may prevent tests from hanging.
- Isolate integration test data. Never run tests against production databases or report skipped tests as passed.
- Add regression tests for transfer behavior changes and bug fixes. Renaming or moving files without behavior changes does not require new tests.
- Use the race detector for concurrency changes. Limit race-free claims to paths actually exercised by tests.

## 9. Coding agent workflow

1. Read repository instructions, the relevant transfer workflow, migrations, and existing tests.
2. Identify affected invariants and the smallest change that satisfies the requirement.
3. Implement within the nearest relevant feature. Preserve existing style and dependencies where adequate.
4. Add or update tests for affected behavior. Do not expand the scope into unnecessary refactoring.
5. Format changed Go files and run relevant verification.
6. Report changes, verification results, skipped tests and their reasons, and remaining limitations.

Baseline checks, adapted to how the repository enables integration tests:

```sh
go test ./...
go vet ./...
# Run for concurrency changes; ensure relevant integration tests are enabled.
go test -race ./...
```

Do not claim integration tests passed when the database was unavailable or tests were skipped. Do not add dependencies or abstractions solely to make tests easier; assess responsibility boundaries first.

## 10. Review checklist

- [ ] A feature change can be traced through closely located code.
- [ ] Handlers, services, and adapters have clear responsibilities.
- [ ] Business logic does not depend on HTTP, SQL, or concrete provider adapters.
- [ ] Interfaces are small, consumer-owned, and have explicit semantics.
- [ ] No unnecessary forwarding layers or speculative abstractions were introduced.
- [ ] Database guarantees protect idempotency and concurrent requests.
- [ ] Timeouts and cancellation are not treated as proof of transfer failure.
- [ ] No network calls occur within database transactions that hold locks.
- [ ] Retries and finalization follow evidence and provider contracts.
- [ ] Tests verify behavior, including affected failure paths.
- [ ] Async tests do not use sleeps for synchronization.
- [ ] Comments explain reasons or invariants that are not obvious from the code.

## 11. Repository adoption

Save this document as `ARCHITECTURE_RULES.md` in the repository root. To make coding agents aware of these rules, add the following to the existing `AGENTS.md` without overwriting other instructions:

```md
For transfer implementation and code review, read and follow ARCHITECTURE_RULES.md.
Make incremental changes. Do not refactor the entire repository merely to match the example structure.
```

If these rules conflict with existing behavior, identify the differences first. Contract or schema changes must be explicit and supported by appropriate verification. User instructions and more specific repository instructions take precedence.
