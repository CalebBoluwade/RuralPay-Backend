-- Add user_id column to transactions table
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS user_id INTEGER REFERENCES users(id);

-- Create index for performance
CREATE INDEX IF NOT EXISTS idx_transactions_user_id ON transactions(user_id);

-- Backfill user_id from accounts table
UPDATE transactions t
SET user_id = a.user_id
FROM accounts a
WHERE t.debit_id = a.card_id AND t.user_id IS NULL;
