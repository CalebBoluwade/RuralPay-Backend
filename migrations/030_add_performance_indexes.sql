-- Index for daily spend calculation in AccountBalanceEnquiry
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_transactions_user_date_debit
ON transactions(user_id, created_at)
WHERE type = 'DEBIT' AND status IN ('PENDING', 'SUCCESS', 'COMPLETED');

-- Index for beneficiaries lookup
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_beneficiaries_user_id
ON beneficiaries(user_id, created_at DESC);
