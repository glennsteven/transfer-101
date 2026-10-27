-- migrate:up
CREATE TABLE IF NOT EXISTS bank_operations (
    operation_id text NOT NULL UNIQUE,
    provider_account_id text NOT NULL,
    provider_reference text NOT NULL CHECK (provider_reference ~ '^[A-Za-z0-9._-]{1,128}$'),
    amount bigint NOT NULL CHECK (amount > 0),
    currency text NOT NULL CHECK (currency = 'IDR'),
    bank_code text NOT NULL CHECK (bank_code ~ '^[0-9]{3}$'),
    account_number text NOT NULL CHECK (account_number ~ '^[0-9]{1,34}$'),
    status text NOT NULL CHECK (status IN ('ACCEPTED', 'SUCCEEDED', 'REJECTED')),
    reason_code text NOT NULL DEFAULT '',
    scenario text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (provider_account_id, provider_reference),
    CHECK ((status = 'REJECTED') = (reason_code <> ''))
);

-- migrate:down
DROP TABLE IF EXISTS bank_operations;
