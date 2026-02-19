-- Create virtual_accounts table for bank-issued virtual accounts
CREATE TABLE IF NOT EXISTS virtual_accounts (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_number VARCHAR(10) UNIQUE NOT NULL,
    account_name VARCHAR(255) NOT NULL,
    bank_name VARCHAR(100) NOT NULL,
    bank_code VARCHAR(10) NOT NULL,
    provider VARCHAR(50) NOT NULL, -- e.g., 'paystack', 'flutterwave', 'wema'
    provider_reference VARCHAR(255), -- Provider's internal reference
    status VARCHAR(20) DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE', 'SUSPENDED')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Indexes
CREATE INDEX idx_virtual_accounts_user_id ON virtual_accounts(user_id);
CREATE INDEX idx_virtual_accounts_account_number ON virtual_accounts(account_number);
CREATE INDEX idx_virtual_accounts_status ON virtual_accounts(status);

-- Ensure one active VA per user
CREATE UNIQUE INDEX idx_virtual_accounts_user_active ON virtual_accounts(user_id) 
WHERE status = 'ACTIVE';
