CREATE TABLE IF NOT EXISTS vouchers (
    id                      SERIAL PRIMARY KEY,
    voucher_code            VARCHAR(50) UNIQUE NOT NULL,
    voucher_description     TEXT NOT NULL,
    voucher_discount_amount NUMERIC(12, 2) NOT NULL,
    voucher_type            VARCHAR(10) NOT NULL CHECK (voucher_type IN ('FIXED', 'PERCENT')),
    voucher_allowed_services TEXT[] NOT NULL DEFAULT '{}',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
