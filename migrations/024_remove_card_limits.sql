-- Remove daily_spent and daily_limit columns from cards table as they are now in user_limits
ALTER TABLE cards DROP COLUMN IF EXISTS daily_spent;
ALTER TABLE cards DROP COLUMN IF EXISTS daily_limit;
