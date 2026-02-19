-- Create merchants table
CREATE TABLE IF NOT EXISTS merchants (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    business_name VARCHAR(255) NOT NULL,
    business_type VARCHAR(100) NOT NULL,
    tax_id VARCHAR(50) NOT NULL UNIQUE,
    account_id VARCHAR(10) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'ACTIVE', 'SUSPENDED', 'REJECTED')),
    commission_rate DECIMAL(5,2) NOT NULL DEFAULT 0.00,
    settlement_cycle VARCHAR(20) NOT NULL DEFAULT 'DAILY' CHECK (settlement_cycle IN ('DAILY', 'WEEKLY', 'MONTHLY')),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(user_id)
);

-- Create index on user_id for faster lookups
CREATE INDEX idx_merchants_user_id ON merchants(user_id);

-- Create index on status for filtering
CREATE INDEX idx_merchants_status ON merchants(status);

-- Create index on account_id for transaction lookups
CREATE INDEX idx_merchants_account_id ON merchants(account_id);
