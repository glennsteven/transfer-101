-- migrate:up
CREATE TABLE IF NOT EXISTS transfers (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9._-]{1,128}$'),
    merchant_id text NOT NULL CHECK (merchant_id ~ '^[A-Za-z0-9._-]{1,64}$'),
    source_account_id text NOT NULL CHECK (source_account_id ~ '^[A-Za-z0-9._-]{1,64}$'),
    provider_account_id text NOT NULL CHECK (provider_account_id ~ '^[A-Za-z0-9._-]{1,64}$'),
    reference_id text NOT NULL CHECK (reference_id ~ '^[A-Za-z0-9._-]{1,64}$'),
    provider_reference text NOT NULL CHECK (provider_reference ~ '^[A-Za-z0-9._-]{1,128}$'),
    amount bigint NOT NULL CHECK (amount > 0),
    currency text NOT NULL DEFAULT 'IDR' CHECK (currency = 'IDR'),
    bank_code text NOT NULL CHECK (bank_code ~ '^[0-9]{3}$'),
    account_number text NOT NULL CHECK (account_number ~ '^[0-9]{1,34}$'),
    status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PROCESSING', 'SUCCESS', 'FAILED')),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT transfers_merchant_reference_key UNIQUE (merchant_id, reference_id),
    CONSTRAINT transfers_provider_reference_key UNIQUE (provider_account_id, provider_reference)
);

-- migrate:down
DROP TABLE IF EXISTS transfers;
