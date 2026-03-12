-- Drop foreign key constraints to allow external account IDs
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_debit_id_fkey;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_credit_id_fkey;
