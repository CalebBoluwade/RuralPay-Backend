-- Update transaction type constraint to include DEBIT, CREDIT, WITHDRAWAL
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_type_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_type_check CHECK (type IN ('DEBIT', 'CREDIT', 'WITHDRAWAL', 'transfer', 'payment', 'refund', 'topup', 'withdrawal'));
