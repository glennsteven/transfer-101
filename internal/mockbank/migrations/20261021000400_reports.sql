-- migrate:up
CREATE TABLE IF NOT EXISTS bank_reports (
    provider_account_id text NOT NULL,
    report_id text NOT NULL,
    csv_content bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (provider_account_id,report_id)
);

-- migrate:down
DROP TABLE IF EXISTS bank_reports;
