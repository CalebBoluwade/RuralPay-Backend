CREATE TABLE IF NOT EXISTS user_email_feedback (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    type       VARCHAR(20) NOT NULL CHECK (type IN ('referral', 'deletion')),
    value      VARCHAR(100) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_email_feedback_user_id ON user_email_feedback (user_id);
CREATE INDEX IF NOT EXISTS idx_user_email_feedback_type    ON user_email_feedback (type);
