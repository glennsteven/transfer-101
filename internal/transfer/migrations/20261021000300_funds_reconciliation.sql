-- migrate:up
CREATE TABLE IF NOT EXISTS merchant_funds (
    merchant_id text NOT NULL,
    source_account_id text NOT NULL,
    opening_amount bigint NOT NULL CHECK (opening_amount >= 0),
    available bigint NOT NULL CHECK (available >= 0),
    held bigint NOT NULL DEFAULT 0 CHECK (held >= 0),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (merchant_id, source_account_id),
    CHECK (available <= 9223372036854775807 - held)
);
ALTER TABLE transfers ADD COLUMN funds_state text NOT NULL DEFAULT 'LEGACY'
    CHECK (funds_state IN ('LEGACY','HELD','CAPTURED','RELEASED'));
ALTER TABLE transfers ALTER COLUMN funds_state SET DEFAULT 'HELD';
ALTER TABLE transfers ADD CONSTRAINT transfers_funds_status CHECK (
    funds_state='LEGACY' OR
    (funds_state='HELD' AND status IN ('PENDING','PROCESSING')) OR
    (funds_state='CAPTURED' AND status='SUCCESS') OR
    (funds_state='RELEASED' AND status='FAILED')
);
-- No opening balances or historical reservations can be inferred from old transfers.
UPDATE transfers SET needs_review=true,review_reason='LEGACY_FUNDS_UNACCOUNTED',
    next_attempt_at=NULL,lease_token=NULL,lease_until=NULL,resubmit_before=NULL
    WHERE status IN ('PENDING','PROCESSING');
CREATE TABLE IF NOT EXISTS fund_movements (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    merchant_id text NOT NULL,
    source_account_id text NOT NULL,
    transfer_id text REFERENCES transfers(id),
    kind text NOT NULL CHECK (kind IN ('OPEN','RESERVE','CAPTURE','RELEASE')),
    amount bigint NOT NULL CHECK (amount >= 0),
    evidence_source text NOT NULL CHECK (evidence_source <> ''),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    FOREIGN KEY (merchant_id,source_account_id) REFERENCES merchant_funds(merchant_id,source_account_id),
    CHECK ((kind='OPEN') = (transfer_id IS NULL)),
    CHECK (kind='OPEN' OR amount>0)
);
CREATE UNIQUE INDEX IF NOT EXISTS fund_open_once ON fund_movements(merchant_id,source_account_id) WHERE kind='OPEN';
CREATE UNIQUE INDEX IF NOT EXISTS fund_reserve_once ON fund_movements(transfer_id) WHERE kind='RESERVE';
CREATE UNIQUE INDEX IF NOT EXISTS fund_settle_once ON fund_movements(transfer_id) WHERE kind IN ('CAPTURE','RELEASE');
CREATE TABLE IF NOT EXISTS reconciliation_sources (
    provider_account_id text NOT NULL,
    report_id text NOT NULL,
    content_hash text NOT NULL,
    PRIMARY KEY (provider_account_id,report_id)
);
CREATE TABLE IF NOT EXISTS reconciliation_imports (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider_account_id text NOT NULL,
    report_id text NOT NULL,
    content_hash text NOT NULL,
    source_hash text NOT NULL,
    verified boolean NOT NULL,
    raw_csv bytea NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    completed_at timestamptz,
    UNIQUE (provider_account_id,report_id,content_hash,source_hash),
    FOREIGN KEY (provider_account_id,report_id) REFERENCES reconciliation_sources(provider_account_id,report_id)
);
CREATE TABLE IF NOT EXISTS reconciliation_findings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    import_id bigint NOT NULL REFERENCES reconciliation_imports(id),
    row_number integer NOT NULL CHECK (row_number >= 0),
    transfer_id text REFERENCES transfers(id),
    code text NOT NULL,
    needs_review boolean NOT NULL,
    decision text NOT NULL,
    before_status text NOT NULL DEFAULT '',
    after_status text NOT NULL DEFAULT '',
    detail jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS reconciliation_findings_import_idx ON reconciliation_findings(import_id,id);

-- migrate:down
DROP TABLE IF EXISTS reconciliation_findings;
DROP TABLE IF EXISTS reconciliation_imports;
DROP TABLE IF EXISTS reconciliation_sources;
DROP TABLE IF EXISTS fund_movements;
DROP TABLE IF EXISTS merchant_funds;
ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_funds_status, DROP COLUMN IF EXISTS funds_state;
-- Schedules disabled by the upgrade are deliberately not restarted by rollback.
