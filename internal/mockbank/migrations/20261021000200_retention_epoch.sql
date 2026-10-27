-- migrate:up
CREATE TABLE IF NOT EXISTS bank_retention (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    epoch text NOT NULL DEFAULT gen_random_uuid()::text
);
INSERT INTO bank_retention (singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

-- migrate:down
DROP TABLE IF EXISTS bank_retention;
