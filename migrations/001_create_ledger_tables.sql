-- Double Ledger System Tables

-- Accounts table with optimistic locking
CREATE TABLE IF NOT EXISTS accounts (
    balance BIGINT NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Ledger entries table for double-entry bookkeeping
CREATE TABLE IF NOT EXISTS ledger_entries (
    id SERIAL PRIMARY KEY,
    transaction_id VARCHAR(255) NOT NULL,
    account_id VARCHAR(255) NOT NULL REFERENCES accounts(id),
    amount BIGINT NOT NULL,
    entry_type VARCHAR(10) NOT NULL CHECK (entry_type IN ('DEBIT', 'CREDIT')),
    balance BIGINT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_ledger_entries_account_id ON ledger_entries(account_id);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_transaction_id ON ledger_entries(transaction_id);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_created_at ON ledger_entries(created_at);
CREATE INDEX IF NOT EXISTS idx_accounts_updated_at ON accounts(updated_at);