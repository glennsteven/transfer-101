-- migrate:up
CREATE TABLE IF NOT EXISTS transfer_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transfer_id text NOT NULL REFERENCES transfers(id),
    started_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    finished_at timestamptz,
    http_status integer CHECK (http_status BETWEEN 100 AND 599),
    bank_status text CHECK (bank_status IN ('ACCEPTED', 'SUCCEEDED', 'REJECTED')),
    outcome text NOT NULL DEFAULT 'STARTED'
        CHECK (outcome IN ('STARTED', 'AMBIGUOUS', 'FINAL_SUCCESS', 'FINAL_REJECTION', 'FINAL_CONFLICT')),
    reason_code text,
    evidence jsonb,
    CHECK ((outcome = 'STARTED') = (finished_at IS NULL))
);
CREATE INDEX IF NOT EXISTS transfer_attempts_transfer_id_idx ON transfer_attempts(transfer_id, id);

-- migrate:down
DROP TABLE IF EXISTS transfer_attempts;
