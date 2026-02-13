# Migration Guide: Transaction Service to Payment Providers

## Overview

This guide helps you migrate from the legacy `TransactionService` to the new payment provider architecture.

## What Changed?

### Before (Legacy)
- Single `TransactionService` handling all payment types
- Payment type determined by request structure
- Endpoint: `POST /api/v1/transactions`

### After (New)
- Multiple specialized payment providers
- Unified `PaymentService` routing to appropriate provider
- Endpoint: `POST /api/v1/payments`
- Legacy endpoints still available for backward compatibility

## Migration Steps

### Step 1: Update API Endpoint

**Old:**
```javascript
POST /api/v1/transactions
```

**New:**
```javascript
POST /api/v1/payments
```

### Step 2: Update Request Structure

#### Card Payment (NFC)

**Old:**
```json
{
  "transaction": {
    "txId": "TX123",
    "cardId": "CARD123",
    "merchantId": "MERCHANT456",
    "amount": 10000,
    "currency": "NGN",
    "counter": 1,
    "txType": "DEBIT",
    "signature": "abc123..."
  }
}
```

**New:**
```json
{
  "transactionId": "TX123",
  "fromAccount": "CARD123",
  "toAccount": "MERCHANT456",
  "amount": 10000,
  "currency": "NGN",
  "paymentMode": "CARD",
  "metadata": {
    "signature": "abc123...",
    "counter": 1
  }
}
```

#### Bank Transfer

**Old:**
```json
{
  "fromAccount": "1234567890",
  "toAccount": "0987654321",
  "toBankCode": "058",
  "amount": 50000,
  "currency": "NGN",
  "reference": "REF123",
  "narration": "Payment"
}
```

**New:**
```json
{
  "fromAccount": "1234567890",
  "toAccount": "0987654321",
  "amount": 50000,
  "currency": "NGN",
  "narration": "Payment",
  "paymentMode": "BANK_TRANSFER",
  "metadata": {
    "toBankCode": "058",
    "reference": "REF123"
  }
}
```

### Step 3: Update Response Handling

**Old Response:**
```json
{
  "success": true,
  "transaction": {
    "txId": "TX123",
    "status": "COMPLETED",
    ...
  }
}
```

**New Response:**
```json
{
  "success": true,
  "transactionId": "TX123",
  "status": "COMPLETED",
  "message": "Payment successful",
  "paymentMode": "CARD",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

## Payment Mode Mapping

| Old Method | New Payment Mode | Notes |
|------------|------------------|-------|
| NFC Card Transaction | `CARD` | Signature verification required |
| External Bank Transfer | `BANK_TRANSFER` | ISO 20022 integration |
| QR Code Payment | `QR` | QR code validation |
| USSD Payment | `USSD` | USSD code validation |
| Voice Payment | `VOICE` | Voice command processing |

## Code Examples

### JavaScript/TypeScript

**Old:**
```typescript
async function processCardPayment(transaction: Transaction) {
  const response = await fetch('/api/v1/transactions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    },
    body: JSON.stringify({ transaction })
  });
  return response.json();
}
```

**New:**
```typescript
async function processPayment(payment: PaymentRequest) {
  const response = await fetch('/api/v1/payments', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    },
    body: JSON.stringify(payment)
  });
  return response.json();
}

// Usage
const result = await processPayment({
  fromAccount: 'CARD123',
  toAccount: 'MERCHANT456',
  amount: 10000,
  currency: 'NGN',
  paymentMode: 'CARD',
  metadata: {
    signature: 'abc123...',
    counter: 1
  }
});
```

### Python

**Old:**
```python
import requests

def process_card_payment(transaction):
    response = requests.post(
        'http://localhost:8080/api/v1/transactions',
        headers={
            'Content-Type': 'application/json',
            'Authorization': f'Bearer {token}'
        },
        json={'transaction': transaction}
    )
    return response.json()
```

**New:**
```python
import requests

def process_payment(payment):
    response = requests.post(
        'http://localhost:8080/api/v1/payments',
        headers={
            'Content-Type': 'application/json',
            'Authorization': f'Bearer {token}'
        },
        json=payment
    )
    return response.json()

# Usage
result = process_payment({
    'fromAccount': 'CARD123',
    'toAccount': 'MERCHANT456',
    'amount': 10000,
    'currency': 'NGN',
    'paymentMode': 'CARD',
    'metadata': {
        'signature': 'abc123...',
        'counter': 1
    }
})
```

### Go

**Old:**
```go
type TransactionRequest struct {
    Transaction Transaction `json:"transaction"`
}

func processCardPayment(tx Transaction) error {
    req := TransactionRequest{Transaction: tx}
    // ... make request
}
```

**New:**
```go
type PaymentRequest struct {
    TransactionID string                 `json:"transactionId"`
    FromAccount   string                 `json:"fromAccount"`
    ToAccount     string                 `json:"toAccount"`
    Amount        int64                  `json:"amount"`
    Currency      string                 `json:"currency"`
    PaymentMode   string                 `json:"paymentMode"`
    Metadata      map[string]interface{} `json:"metadata"`
}

func processPayment(payment PaymentRequest) error {
    // ... make request to /api/v1/payments
}
```

## Backward Compatibility

The legacy TransactionService has been removed. All payment operations now use the unified payment provider architecture:
- `POST /api/v1/payments` - All payment types (specify `paymentMode`)
- `GET /api/v1/transactions` - Query transactions (read-only)
- `GET /api/v1/transactions/{txId}` - Get specific transaction
- `GET /api/v1/transactions/recent` - Get recent transactions
- `GET /api/v1/accounts/name-enquiry` - Account name lookup
- `GET /api/v1/accounts/balance-enquiry` - Account balance lookup

## Testing Your Migration

### 1. Test Card Payment
```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "fromAccount": "CARD123",
    "toAccount": "MERCHANT456",
    "amount": 10000,
    "currency": "NGN",
    "paymentMode": "CARD",
    "metadata": {
      "signature": "your_signature",
      "counter": 1
    }
  }'
```

### 2. Test Bank Transfer
```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "fromAccount": "1234567890",
    "toAccount": "0987654321",
    "amount": 50000,
    "currency": "NGN",
    "narration": "Test payment",
    "paymentMode": "BANK_TRANSFER",
    "metadata": {
      "toBankCode": "058"
    }
  }'
```

### 3. Test QR Payment
```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "fromAccount": "ACC123",
    "toAccount": "MERCHANT456",
    "amount": 5000,
    "currency": "NGN",
    "paymentMode": "QR",
    "metadata": {
      "qrCode": "your_qr_code_data"
    }
  }'
```

## Common Issues and Solutions

### Issue 1: "Unsupported payment mode"
**Solution:** Ensure `paymentMode` is one of: `CARD`, `QR`, `BANK_TRANSFER`, `USSD`, `VOICE`

### Issue 2: "Unable To Process This Request At This Time"
**Solution:** Check that your JSON structure matches the new `PaymentRequest` format

### Issue 3: "Account does not belong to user"
**Solution:** Verify that the `fromAccount` belongs to the authenticated user

### Issue 4: Missing metadata
**Solution:** Each payment mode requires specific metadata:
- `CARD`: signature, counter
- `QR`: qrCode
- `BANK_TRANSFER`: toBankCode (optional)
- `USSD`: ussdCode, codeType
- `VOICE`: voiceCommand

## Rollback Plan

If you need to rollback:
1. Revert to using legacy endpoints (`/api/v1/transactions`)
2. Use old request/response structures
3. Legacy endpoints will remain available until announced deprecation date

## Support

For migration assistance:
- Check the [Payment Providers Documentation](./PAYMENT_PROVIDERS.md)
- Review API examples in the `/examples` directory
- Contact the development team

## Timeline

- **Completed**: Migration to payment provider architecture
- **Current**: Unified `/api/v1/payments` endpoint for all payment types
- **Legacy endpoints**: Removed - use new payment provider architecture
