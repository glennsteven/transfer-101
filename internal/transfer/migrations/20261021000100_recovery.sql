-- migrate:up
ALTER TABLE transfers
    ADD COLUMN next_attempt_at timestamptz DEFAULT statement_timestamp(),
    ADD COLUMN job_kind text NOT NULL DEFAULT 'SUBMIT' CHECK (job_kind IN ('SUBMIT', 'INQUIRY')),
    ADD COLUMN recovery_attempts integer NOT NULL DEFAULT 0 CHECK (recovery_attempts >= 0),
    ADD COLUMN recovery_max_attempts integer NOT NULL DEFAULT 8 CHECK (recovery_max_attempts BETWEEN 1 AND 100),
    ADD COLUMN lease_token text,
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN last_error text NOT NULL DEFAULT '',
    ADD COLUMN needs_review boolean NOT NULL DEFAULT false,
    ADD COLUMN review_reason text NOT NULL DEFAULT '',
    ADD COLUMN retention_epoch text,
    ADD COLUMN resubmit_before timestamptz DEFAULT (statement_timestamp() + interval '15 minutes'),
    ADD CONSTRAINT transfers_lease_pair CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    ADD CONSTRAINT transfers_review_reason CHECK (needs_review = (review_reason <> ''));

-- Historical PROCESSING rows have no durable proof of their original bank dataset.
UPDATE transfers SET job_kind='INQUIRY', resubmit_before=NULL WHERE status='PROCESSING';
UPDATE transfers SET next_attempt_at=NULL WHERE status IN ('SUCCESS','FAILED');
ALTER TABLE transfer_attempts
    ADD COLUMN lease_token text,
    ADD COLUMN kind text NOT NULL DEFAULT 'SUBMIT' CHECK (kind IN ('SUBMIT','INQUIRY','RESUBMIT'));
CREATE INDEX IF NOT EXISTS transfers_due_idx ON transfers(next_attempt_at, id)
    WHERE status IN ('PENDING','PROCESSING') AND NOT needs_review;

-- migrate:down
DROP INDEX IF EXISTS transfers_due_idx;
ALTER TABLE transfer_attempts DROP COLUMN IF EXISTS lease_token, DROP COLUMN IF EXISTS kind;
ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_lease_pair, DROP CONSTRAINT IF EXISTS transfers_review_reason,
    DROP COLUMN IF EXISTS next_attempt_at, DROP COLUMN IF EXISTS job_kind,
    DROP COLUMN IF EXISTS recovery_attempts, DROP COLUMN IF EXISTS recovery_max_attempts,
    DROP COLUMN IF EXISTS lease_token, DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS last_error, DROP COLUMN IF EXISTS needs_review, DROP COLUMN IF EXISTS review_reason,
    DROP COLUMN IF EXISTS retention_epoch, DROP COLUMN IF EXISTS resubmit_before;
