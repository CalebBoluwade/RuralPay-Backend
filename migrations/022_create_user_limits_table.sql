CREATE TABLE IF NOT EXISTS user_limits (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    daily_limit BIGINT NOT NULL DEFAULT 500000,
    single_transaction_limit BIGINT NOT NULL DEFAULT 100000,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id)
);

CREATE INDEX idx_user_limits_user_id ON user_limits(user_id);
