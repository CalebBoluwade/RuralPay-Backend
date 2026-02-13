# Payment Provider Architecture

## Overview

The payment system has been refactored into a provider-based architecture that supports multiple payment modes through a unified interface. This design allows for easy extension and maintenance of different payment types.

## Payment Modes

The system supports the following payment modes:

1. **CARD** - NFC card-based payments
2. **QR** - QR code-based payments
3. **BANK_TRANSFER** - Bank-to-bank transfers (ISO 20022)
4. **USSD** - USSD code-based payments
5. **VOICE** - Voice command-based payments

## Architecture

### Core Components

#### 1. PaymentProvider Interface
```go
type PaymentProvider interface {
    ProcessPayment(ctx context.Context, req *PaymentRequest) (*PaymentResponse, error)
    ValidatePayment(ctx context.Context, req *PaymentRequest) error
    GetPaymentMode() PaymentMode
}
```

#### 2. PaymentService
Central service that manages all payment providers and routes requests to the appropriate provider based on payment mode.

#### 3. Individual Payment Providers
- `CardPaymentProvider` - Handles NFC card payments with signature verification
- `QRPaymentProvider` - Processes QR code payments
- `BankTransferPaymentProvider` - Manages bank transfers with ISO 20022 integration
- `USSDPaymentProvider` - Handles USSD code-based payments
- `VoicePaymentProvider` - Processes voice command payments

## API Usage

### Endpoint
```
POST /payments
```

### Request Structure
```json
{
  "transactionId": "PAY-1234567890",
  "fromAccount": "1234567890",
  "toAccount": "0987654321",
  "amount": 10000,
  "currency": "NGN",
  "narration": "Payment description",
  "paymentMode": "CARD|QR|BANK_TRANSFER|USSD|VOICE",
  "metadata": {
    // Mode-specific data
  },
  "location": {
    "latitude": 6.5244,
    "longitude": 3.3792,
    "accuracy": 10.0,
    "address": "Lagos, Nigeria"
  }
}
```

### Response Structure
```json
{
  "success": true,
  "transactionId": "PAY-1234567890",
  "status": "COMPLETED|PENDING|FAILED",
  "message": "Payment successful",
  "paymentMode": "CARD",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

## Payment Mode Specific Requirements

### 1. CARD Payment
```json
{
  "paymentMode": "CARD",
  "fromAccount": "card_id",
  "metadata": {
    "signature": "hex_encoded_signature",
    "counter": 123
  }
}
```

### 2. QR Payment
```json
{
  "paymentMode": "QR",
  "metadata": {
    "qrCode": "base64_encoded_qr_data"
  }
}
```

### 3. BANK_TRANSFER Payment
```json
{
  "paymentMode": "BANK_TRANSFER",
  "metadata": {
    "toBankCode": "058",
    "reference": "REF123"
  }
}
```

### 4. USSD Payment
```json
{
  "paymentMode": "USSD",
  "metadata": {
    "ussdCode": "123456",
    "codeType": "PUSH|PULL"
  }
}
```

### 5. VOICE Payment
```json
{
  "paymentMode": "VOICE",
  "metadata": {
    "voiceCommand": "pay 100 naira to john",
    "audioTranscript": "transcribed text",
    "confidence": 0.95
  }
}
```

## Features

### 1. Idempotency
All payment requests are idempotent. Duplicate transaction IDs will return the cached result without reprocessing.

### 2. Account Ownership Verification
The system verifies that the source account belongs to the authenticated user before processing.

### 3. Balance Validation
All providers validate sufficient balance before processing payments.

### 4. Transaction Logging
All transactions are logged with full audit trail including:
- Transaction ID
- Payment mode
- Source and destination accounts
- Amount and fees
- Status and timestamps
- Metadata and location

### 5. Fee Calculation
Bank transfers include automatic fee calculation:
- Percentage-based fee (default: 0.5%)
- Fixed fee (default: 50 kobo)

### 6. Settlement Integration
Bank transfers are automatically sent to ISO 20022 settlement system.

## Security Features

### 1. Authentication
All endpoints require user authentication via JWT token.

### 2. Signature Verification (Card Payments)
Card payments verify HMAC-SHA256 signatures using card authentication keys.

### 3. Code Validation (USSD/QR)
USSD and QR codes are validated and consumed (single-use) during payment processing.

### 4. Rate Limiting
Redis-based rate limiting prevents abuse.

### 5. Replay Attack Prevention
Transaction IDs are tracked in Redis to prevent replay attacks.

## Error Handling

### Common Error Responses
- `400 Bad Request` - Invalid request data or validation failure
- `401 Unauthorized` - Missing or invalid authentication
- `403 Forbidden` - Account ownership verification failed
- `404 Not Found` - Account or resource not found
- `500 Internal Server Error` - Server-side processing error

### Error Response Format
```json
{
  "error": "Error message",
  "details": "Additional error details"
}
```

## Database Schema

### Transactions Table
```sql
CREATE TABLE transactions (
    id SERIAL PRIMARY KEY,
    transaction_id VARCHAR(255) UNIQUE NOT NULL,
    from_card_id VARCHAR(255),
    to_card_id VARCHAR(255),
    amount BIGINT NOT NULL,
    fee BIGINT DEFAULT 0,
    total_amount BIGINT,
    currency VARCHAR(3) DEFAULT 'NGN',
    narration TEXT,
    type VARCHAR(20),
    status VARCHAR(50),
    signature TEXT,
    location JSONB,
    metadata JSONB,
    user_id INTEGER,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);
```

## Testing

### Example Card Payment
```bash
curl -X POST http://localhost:8080/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "fromAccount": "CARD123",
    "toAccount": "MERCHANT456",
    "amount": 10000,
    "currency": "NGN",
    "paymentMode": "CARD",
    "metadata": {
      "signature": "abc123...",
      "counter": 1
    }
  }'
```

### Example QR Payment
```bash
curl -X POST http://localhost:8080/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "fromAccount": "ACC123",
    "toAccount": "MERCHANT456",
    "amount": 5000,
    "currency": "NGN",
    "paymentMode": "QR",
    "metadata": {
      "qrCode": "eyJ1c2VySWQiOiIxMjMi..."
    }
  }'
```

### Example Bank Transfer
```bash
curl -X POST http://localhost:8080/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "fromAccount": "1234567890",
    "toAccount": "0987654321",
    "amount": 50000,
    "currency": "NGN",
    "narration": "Salary payment",
    "paymentMode": "BANK_TRANSFER",
    "metadata": {
      "toBankCode": "058"
    }
  }'
```

## Extension Guide

### Adding a New Payment Provider

1. Create a new provider file (e.g., `mobile_money_payment_provider.go`)
2. Implement the `PaymentProvider` interface
3. Register the provider in `PaymentService.NewPaymentService()`

Example:
```go
type MobileMoneyPaymentProvider struct {
    *BasePaymentProvider
}

func (p *MobileMoneyPaymentProvider) GetPaymentMode() PaymentMode {
    return PaymentModeMobileMoney
}

func (p *MobileMoneyPaymentProvider) ValidatePayment(ctx context.Context, req *PaymentRequest) error {
    // Validation logic
}

func (p *MobileMoneyPaymentProvider) ProcessPayment(ctx context.Context, req *PaymentRequest) (*PaymentResponse, error) {
    // Processing logic
}
```

## Migration from Old Transaction Service

The old `TransactionService` is still available for backward compatibility. To migrate:

1. Update client code to use `/payments` endpoint instead of `/transactions`
2. Include `paymentMode` in request body
3. Update response handling to use new `PaymentResponse` structure
4. Test thoroughly before deprecating old endpoints

## Performance Considerations

1. **Redis Caching** - Idempotency checks use Redis for fast lookups
2. **Database Transactions** - All payment processing uses database transactions for consistency
3. **Async Settlement** - Bank transfers send to settlement asynchronously
4. **Connection Pooling** - Database connections are pooled for efficiency

## Monitoring and Logging

All payment operations are logged with:
- Transaction ID
- User ID
- Payment mode
- Amount and currency
- Status and timestamps
- Error details (if any)

Log format:
```
[PAYMENT] Processing CARD payment: txID=PAY-123, from=CARD123, to=MERCHANT456, amount=10000
[PAYMENT] Payment successful: txID=PAY-123, status=COMPLETED
```

## Support

For issues or questions, contact the development team or refer to the main project documentation.
