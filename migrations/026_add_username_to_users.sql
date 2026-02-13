-- Add username column to users table
ALTER TABLE users ADD COLUMN IF NOT EXISTS username VARCHAR(50) UNIQUE;

-- Create index for username lookups
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

-- Create index for phone_number lookups
CREATE INDEX IF NOT EXISTS idx_users_phone_number ON users(phone_number);
