CREATE TABLE IF NOT EXISTS beneficiaries (
    id             SERIAL PRIMARY KEY,
    user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_number VARCHAR(20) NOT NULL,
    account_name   VARCHAR(100) NOT NULL,
    bank_name      VARCHAR(100) NOT NULL,
    bank_code      VARCHAR(10) NOT NULL,
    created_at     TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, account_number, bank_code)
);

CREATE INDEX IF NOT EXISTS idx_beneficiaries_user_id ON beneficiaries (user_id);
