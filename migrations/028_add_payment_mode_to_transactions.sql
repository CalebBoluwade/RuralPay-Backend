-- Add payment_mode column to transactions table
ALTER TABLE transactions 
ADD COLUMN payment_mode VARCHAR(20) DEFAULT 'BANK_TRANSFER'
CHECK (payment_mode IN ('CARD', 'QR', 'BANK_TRANSFER', 'USSD', 'VOICE', 'AIRTIME_DATA'));


CREATE TYPE payment_mode_enum AS ENUM
    ('CARD','QR','BANK_TRANSFER','USSD','VOICE','AIRTIME_DATA');

ALTER TABLE transactions
    ADD COLUMN payment_mode payment_mode_enum DEFAULT 'BANK_TRANSFER';