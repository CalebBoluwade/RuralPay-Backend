-- HSM keys table for cryptographic key management
CREATE TABLE IF NOT EXISTS hsm_keys (
    id SERIAL PRIMARY KEY,
    key_id VARCHAR(255) UNIQUE NOT NULL,
    key_type VARCHAR(50) NOT NULL CHECK (key_type IN ('RSA', 'AES', 'ECDSA')),
    key_usage VARCHAR(50) NOT NULL CHECK (key_usage IN ('transaction_signing')),
    key_size INTEGER NOT NULL,
    public_key TEXT,
    encrypted_private_key TEXT,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMP NOT NULL,
    rotated_at TIMESTAMP,
    metadata JSONB
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_hsm_keys_key_id ON hsm_keys(key_id);
CREATE INDEX IF NOT EXISTS idx_hsm_keys_key_usage ON hsm_keys(key_usage);
CREATE INDEX IF NOT EXISTS idx_hsm_keys_is_active ON hsm_keys(is_active);
CREATE INDEX IF NOT EXISTS idx_hsm_keys_expires_at ON hsm_keys(expires_at);
CREATE INDEX IF NOT EXISTS idx_hsm_keys_created_at ON hsm_keys(created_at);