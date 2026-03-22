CREATE TABLE IF NOT EXISTS transaction_feedback (
    id             SERIAL PRIMARY KEY,
    transaction_id VARCHAR(100) NOT NULL,
    email          VARCHAR(255) NOT NULL,
    rating         VARCHAR(3)   NOT NULL CHECK (rating IN ('yes', 'no')),
    created_at     TIMESTAMP    NOT NULL DEFAULT NOW(),
    UNIQUE (transaction_id, email)
);

CREATE INDEX IF NOT EXISTS idx_transaction_feedback_transaction_id ON transaction_feedback (transaction_id);
