-- Cards table for NFC card management
CREATE TABLE IF NOT EXISTS cards (
    id SERIAL PRIMARY KEY,
    card_id VARCHAR(255) UNIQUE NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id),
    serial_number VARCHAR(255) UNIQUE NOT NULL,
    balance DECIMAL(15,2) NOT NULL DEFAULT 0.00,
    currency VARCHAR(3) NOT NULL DEFAULT 'NGN',
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'blocked', 'lost', 'expired')),
    card_type VARCHAR(50) NOT NULL DEFAULT 'standard',
    last_sync_at TIMESTAMP,
    last_transaction_at TIMESTAMP,
    tx_counter INTEGER NOT NULL DEFAULT 0,
    max_balance DECIMAL(15,2) NOT NULL DEFAULT 10000.00,
    daily_spent DECIMAL(15,2) NOT NULL DEFAULT 0.00,
    metadata JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_cards_card_id ON cards(card_id);
CREATE INDEX IF NOT EXISTS idx_cards_user_id ON cards(user_id);
CREATE INDEX IF NOT EXISTS idx_cards_serial_number ON cards(serial_number);
CREATE INDEX IF NOT EXISTS idx_cards_status ON cards(status);
CREATE INDEX IF NOT EXISTS idx_cards_last_sync_at ON cards(last_sync_at);
CREATE INDEX IF NOT EXISTS idx_cards_created_at ON cards(created_at);