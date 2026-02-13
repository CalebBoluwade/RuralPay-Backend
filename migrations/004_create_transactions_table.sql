-- Transactions table for payment transactions
CREATE TABLE IF NOT EXISTS transactions (
    id SERIAL PRIMARY KEY,
    transaction_id VARCHAR(255) UNIQUE NOT NULL,
    reference_id VARCHAR(255),
    from_card_id VARCHAR(255) REFERENCES cards(card_id),
    to_card_id VARCHAR(255) REFERENCES cards(card_id),
    amount DECIMAL(15,2) NOT NULL,
    fee DECIMAL(15,2) NOT NULL DEFAULT 0.00,
    total_amount DECIMAL(15,2) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'NGN',
    narration VARCHAR(200),
    status VARCHAR(25) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PROCESSING', 'COMPLETED', 'FAILED', 'CANCELLED', 'ACCOUNT_NOT_FOUND', 'ACCOUNT_NOT_ACTIVE', 'INSUFFICIENT_BALANCE', 'FAILED_DEBIT', 'FAILED_ISO_CONVERT', 'FAILED_SETTLEMENT')),
    type VARCHAR(50) NOT NULL CHECK (type IN ('DEBIT', 'CREDIT', 'WITHDRAWAL')),
    signature TEXT,
    device_id VARCHAR(255),
    location JSONB,
    sync_status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (sync_status IN ('PENDING', 'SYNCED', 'FAILED')),
    error_message TEXT,
    metadata JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    settled_at TIMESTAMP,
    processed_at TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_transactions_transaction_id ON transactions(transaction_id);
CREATE INDEX IF NOT EXISTS idx_transactions_from_card_id ON transactions(from_card_id);
CREATE INDEX IF NOT EXISTS idx_transactions_to_card_id ON transactions(to_card_id);
CREATE INDEX IF NOT EXISTS idx_transactions_status ON transactions(status);
CREATE INDEX IF NOT EXISTS idx_transactions_type ON transactions(type);
CREATE INDEX IF NOT EXISTS idx_transactions_sync_status ON transactions(sync_status);
CREATE INDEX IF NOT EXISTS idx_transactions_created_at ON transactions(created_at);
CREATE INDEX IF NOT EXISTS idx_transactions_settled_at ON transactions(settled_at);