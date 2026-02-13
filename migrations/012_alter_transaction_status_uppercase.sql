-- Alter transaction status constraint to accept uppercase values
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_status_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_status_check 
    CHECK (status IN ('PENDING', 'PROCESSING', 'COMPLETED', 'FAILED', 'CANCELLED', 'ACCOUNT_NOT_FOUND', 'ACCOUNT_NOT_ACTIVE', 'INSUFFICIENT_BALANCE', 'FAILED_DEBIT', 'FAILED_ISO_CONVERT', 'FAILED_SETTLEMENT'));
