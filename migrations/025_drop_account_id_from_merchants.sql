-- Drop account_id column from merchants table
DROP INDEX IF EXISTS idx_merchants_account_id;
ALTER TABLE merchants DROP COLUMN IF EXISTS account_id;
