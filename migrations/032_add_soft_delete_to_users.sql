-- Soft delete support for users table
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deletion_reason VARCHAR(50) NULL
    CHECK (deletion_reason IN ('USER_REQUEST', 'ADMIN', 'COMPLIANCE'));

-- Partial index: unique email/phone only among non-deleted users
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_active
    ON users (email) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone_active
    ON users (phone_number) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_users_deleted_at
    ON users (deleted_at) WHERE deleted_at IS NOT NULL;
