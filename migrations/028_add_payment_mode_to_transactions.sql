-- Add payment_mode column to transactions table
ALTER TABLE transactions 
ADD COLUMN payment_mode VARCHAR(20) DEFAULT 'CARD' 
CHECK (payment_mode IN ('CARD', 'QR', 'BANK_TRANSFER', 'USSD', 'VOICE'));
