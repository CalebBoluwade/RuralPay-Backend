-- Audit events table for HSM security logging
CREATE TABLE IF NOT EXISTS audit_events (
    timestamp TIMESTAMP NOT NULL DEFAULT NOW(),
    event_type VARCHAR(50) NOT NULL,
    transaction_id VARCHAR(255),
    account_id VARCHAR(255),
    amount BIGINT,
    status VARCHAR(20) NOT NULL,
    details JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Indexes for performance and audit queries
CREATE INDEX IF NOT EXISTS idx_audit_events_timestamp ON audit_events(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_events_event_type ON audit_events(event_type);
CREATE INDEX IF NOT EXISTS idx_audit_events_transaction_id ON audit_events(transaction_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_account_id ON audit_events(account_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_status ON audit_events(status);