# Payment Providers Package

This package contains all payment provider implementations for the NFC Payments Backend system.

## Structure

```
providers/
├── provider.go                    # Base interfaces and types
├── card_provider.go              # Card/NFC payment provider
├── qr_provider.go                # QR code payment provider
├── bank_transfer_provider.go    # Bank transfer payment provider
├── ussd_provider.go              # USSD payment provider
└── voice_provider.go             # Voice payment provider
```

## Core Components

### provider.go

- `PaymentMode` - Enum for payment types (CARD, QR, BANK_TRANSFER, USSD, VOICE)
- `PaymentRequest` - Unified payment request structure
- `PaymentResponse` - Unified payment response structure
- `PaymentProvider` - Interface that all providers must implement
- `BasePaymentProvider` - Base struct with common dependencies

### Provider Interface

```go
type PaymentProvider interface {
    ProcessPayment(ctx context.Context, req *PaymentRequest) (*PaymentResponse, error)
    ValidatePayment(ctx context.Context, req *PaymentRequest) error
    GetPaymentMode() PaymentMode
}
```

## Individual Providers

### 1. Card Provider (card_provider.go)

Handles NFC card-based payments with HMAC signature verification.

**Features:**

- Card authentication key (CAK) verification
- HMAC-SHA256 signature validation
- Balance checking
- Ledger transfer integration

### 2. QR Provider (qr_provider.go)

Processes QR code-based payments.

**Features:**

- QR code validation and consumption
- User ownership verification
- Single-use QR codes
- Redis-based expiration

### 3. Bank Transfer Provider (bank_transfer_provider.go)

Manages bank-to-bank transfers with ISO 20022 integration.

**Features:**

- Fee calculation (percentage + fixed)
- ISO 20022 message generation
- Asynchronous settlement
- External bank integration

### 4. USSD Provider (ussd_provider.go)

Handles USSD code-based payments.

**Features:**

- USSD code validation and consumption
- Push/Pull payment types
- Single-use codes
- Amount verification

### 5. Voice Provider (voice_provider.go)

Processes voice command-based payments.

**Features:**

- Voice command validation
- Natural language processing integration
- Command pattern matching
- Audio transcription support

## Usage

### Creating a Provider

```go
import "github.com/ruralpay/backend/internal/providers"

// Initialize provider
cardProvider := providers.NewCardPaymentProvider(db, redis, hsm)

// Process payment
response, err := cardProvider.ProcessPayment(ctx, &providers.PaymentRequest{
    TransactionID: "TX123",
    FromAccount:   "CARD123",
    ToAccount:     "MERCHANT456",
    Amount:        10000,
    Currency:      "NGN",
    PaymentMode:   providers.PaymentModeCard,
    Metadata: map[string]interface{}{
        "signature": "abc123...",
        "counter":   1,
    },
})
```

### Adding a New Provider

1. Create a new file (e.g., `mobile_money_provider.go`)
2. Define the provider struct embedding `BasePaymentProvider`
3. Implement the `PaymentProvider` interface:
   - `GetPaymentMode()`
   - `ValidatePayment()`
   - `ProcessPayment()`
4. Register in `PaymentService` (in services package)

Example:

```go
package providers

type MobileMoneyProvider struct {
    *BasePaymentProvider
}

func NewMobileMoneyProvider(db *sql.DB, redis *redis.Client, hsm hsm.HSMInterface) *MobileMoneyProvider {
    return &MobileMoneyProvider{
        BasePaymentProvider: NewBasePaymentProvider(db, redis, hsm),
    }
}

func (p *MobileMoneyProvider) GetPaymentMode() PaymentMode {
    return PaymentModeMobileMoney
}

func (p *MobileMoneyProvider) ValidatePayment(ctx context.Context, req *PaymentRequest) error {
    // Validation logic
}

func (p *MobileMoneyProvider) ProcessPayment(ctx context.Context, req *PaymentRequest) (*PaymentResponse, error) {
    // Processing logic
}
```

## Dependencies

Each provider has access to:

- `DB` - PostgreSQL database connection
- `Redis` - Redis client for caching
- `HSM` - Hardware Security Module interface
- `Ledger` - Double-entry ledger service
- `Validator` - Request validation helper

## Testing

Each provider should have corresponding tests:

```
providers/
├── card_provider_test.go
├── qr_provider_test.go
├── bank_transfer_provider_test.go
├── ussd_provider_test.go
└── voice_provider_test.go
```

## Error Handling

All providers return standardized errors:

- Validation errors return `PaymentResponse` with `Success: false`
- Processing errors return Go errors
- All errors are logged via audit logger

## Security

- All providers validate account ownership
- Balance checks before processing
- Idempotency via Redis caching
- Audit logging for all operations
- Signature verification where applicable

## Related Documentation

- [Payment Providers Documentation](../../docs/PAYMENT_PROVIDERS.md)
- [Migration Guide](../../docs/MIGRATION_GUIDE.md)
- [Quick Reference](../../docs/PAYMENT_QUICK_REFERENCE.md)
