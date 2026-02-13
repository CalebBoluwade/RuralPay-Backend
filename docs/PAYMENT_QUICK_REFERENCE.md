# Payment Providers Quick Reference

## Endpoint
```
POST /api/v1/payments
```

## Authentication
All requests require JWT token in Authorization header:
```
Authorization: Bearer <your_jwt_token>
```

## Payment Modes

### 1. CARD (NFC Card Payment)
```json
{
  "fromAccount": "CARD_ID",
  "toAccount": "MERCHANT_ID",
  "amount": 10000,
  "currency": "NGN",
  "paymentMode": "CARD",
  "metadata": {
    "signature": "hmac_sha256_signature",
    "counter": 1
  }
}
```

### 2. QR (QR Code Payment)
```json
{
  "fromAccount": "ACCOUNT_ID",
  "toAccount": "MERCHANT_ID",
  "amount": 5000,
  "currency": "NGN",
  "paymentMode": "QR",
  "metadata": {
    "qrCode": "base64_encoded_qr_data"
  }
}
```

### 3. BANK_TRANSFER (Bank Transfer)
```json
{
  "fromAccount": "1234567890",
  "toAccount": "0987654321",
  "amount": 50000,
  "currency": "NGN",
  "narration": "Payment description",
  "paymentMode": "BANK_TRANSFER",
  "metadata": {
    "toBankCode": "058",
    "reference": "REF123"
  },
  "location": {
    "latitude": 6.5244,
    "longitude": 3.3792
  }
}
```

### 4. USSD (USSD Code Payment)
```json
{
  "fromAccount": "ACCOUNT_ID",
  "toAccount": "MERCHANT_ID",
  "amount": 3000,
  "currency": "NGN",
  "paymentMode": "USSD",
  "metadata": {
    "ussdCode": "123456",
    "codeType": "PUSH"
  }
}
```

### 5. VOICE (Voice Command Payment)
```json
{
  "fromAccount": "ACCOUNT_ID",
  "toAccount": "MERCHANT_ID",
  "amount": 2000,
  "currency": "NGN",
  "paymentMode": "VOICE",
  "metadata": {
    "voiceCommand": "pay 20 naira to john",
    "audioTranscript": "transcribed text",
    "confidence": 0.95
  }
}
```

## Response Format

### Success Response
```json
{
  "success": true,
  "transactionId": "PAY-1234567890",
  "status": "COMPLETED",
  "message": "Payment successful",
  "paymentMode": "CARD",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

### Pending Response (Bank Transfers)
```json
{
  "success": true,
  "transactionId": "PAY-1234567890",
  "status": "PENDING",
  "message": "Bank transfer initiated",
  "paymentMode": "BANK_TRANSFER",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

### Error Response
```json
{
  "success": false,
  "transactionId": "PAY-1234567890",
  "status": "FAILED",
  "message": "Insufficient balance",
  "paymentMode": "CARD",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

## Status Codes

| Code | Meaning |
|------|---------|
| 200 | Success |
| 400 | Bad Request (validation error) |
| 401 | Unauthorized (missing/invalid token) |
| 403 | Forbidden (account ownership error) |
| 404 | Not Found (account not found) |
| 500 | Internal Server Error |

## Common Error Messages

| Error | Cause | Solution |
|-------|-------|----------|
| "Unauthorized" | Missing/invalid JWT token | Include valid token in Authorization header |
| "Account does not belong to user" | fromAccount not owned by user | Use correct account ID |
| "Insufficient balance" | Not enough funds | Top up account |
| "Invalid QR code" | QR code expired/invalid | Generate new QR code |
| "USSD code already used" | Code consumed | Generate new USSD code |
| "Unsupported payment mode" | Invalid paymentMode | Use valid mode: CARD, QR, BANK_TRANSFER, USSD, VOICE |

## Field Validation

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| fromAccount | string | Yes | Must belong to authenticated user |
| toAccount | string | Yes | Valid account/merchant ID |
| amount | int64 | Yes | > 0 (in kobo/cents) |
| currency | string | Yes | 3-letter code (e.g., "NGN") |
| paymentMode | string | Yes | One of: CARD, QR, BANK_TRANSFER, USSD, VOICE |
| narration | string | No | Max 200 characters |
| metadata | object | Yes | Mode-specific data |
| location | object | No | GPS coordinates |

## Amount Format

Amounts are in the smallest currency unit (kobo for NGN):
- ₦100.00 = 10000 kobo
- ₦1.50 = 150 kobo
- ₦0.50 = 50 kobo

## Idempotency

Duplicate `transactionId` values return cached result without reprocessing:
```json
{
  "success": true,
  "transactionId": "PAY-1234567890",
  "status": "COMPLETED",
  "message": "Payment already processed",
  "paymentMode": "CARD"
}
```

## Rate Limits

- USSD code generation: 10 per user per hour
- QR code generation: 20 per user per hour
- Payment requests: 100 per user per hour

## Testing

### Test Card Payment
```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer TOKEN" \
  -d '{"fromAccount":"CARD123","toAccount":"MERCHANT456","amount":10000,"currency":"NGN","paymentMode":"CARD","metadata":{"signature":"sig","counter":1}}'
```

### Test Bank Transfer
```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer TOKEN" \
  -d '{"fromAccount":"1234567890","toAccount":"0987654321","amount":50000,"currency":"NGN","paymentMode":"BANK_TRANSFER","metadata":{"toBankCode":"058"}}'
```

## Related Endpoints

### Generate QR Code
```
POST /api/v1/qr/generate
```

### Generate USSD Code
```
POST /api/v1/ussd/generate
```

### Get Transaction History
```
GET /api/v1/transactions/recent?limit=10
```

### Account Balance
```
GET /api/v1/accounts/balance-enquiry
```

## Support

- Documentation: `/docs/PAYMENT_PROVIDERS.md`
- Migration Guide: `/docs/MIGRATION_GUIDE.md`
- API Docs: `http://localhost:8080/swagger/`
