-- Create admins table
CREATE TABLE IF NOT EXISTS admins (
    user_id INTEGER NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_admins_user_id FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_admins_user_id ON admins(user_id);
-- CREATE INDEX IF NOT EXISTS idx_admins_created_at ON admins(created_at);

-- Add comment
COMMENT ON TABLE admins IS 'Table to track admin users';
COMMENT ON COLUMN admins.user_id IS 'Reference to the user who is an admin';
