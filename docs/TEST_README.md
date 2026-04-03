# Unit Tests Documentation

This document describes the unit tests for the NFC Payments Backend services.

## Test Structure

The tests are organized by service, with each service having its own test file:

- `auth_test.go` - Tests for AuthService (registration, login, password hashing, JWT)
- `card_provisioning_service_test.go` - Tests for CardProvisioningService (provisioning, activation, management)
- `hsm_key_service_test.go` - Tests for HSMKeyService (key synchronization, database operations)
- `ledger_service_test.go` - Tests for DoubleLedgerService (double-entry bookkeeping, transfers)
- `transaction_service_test.go` - Tests for TransactionService (transaction processing, validation, enquiries)
- `iso20022_service_test.go` - Tests for ISO20022Service (message conversion, settlement processing)
- `validation_test.go` - Tests for ValidationHelper (validation, error responses)

## Test Coverage

Each test file covers:

### AuthService Tests
- User registration (successful, validation errors, duplicate email)
- User login (successful, invalid credentials, user not found)
- Password hashing and verification
- JWT token generation

### CardProvisioningService Tests
- Card provisioning (successful, invalid card type, validation errors)
- Card activation (successful, invalid activation code)
- Card management (get, suspend, reinstate)

### HSMKeyService Tests
- Key synchronization to database (successful, HSM errors)
- Key type and size determination
- Database upsert operations

### DoubleLedgerService Tests
- Money transfers (successful, insufficient balance, account creation)
- Account locking and optimistic locking
- Ledger entry creation
- Balance updates

### TransactionService Tests
- Transaction creation (successful, validation errors, double spending)
- Account name enquiry (successful, not found, inactive account)
- Account balance enquiry (successful, validation)
- Transaction validation (valid, missing fields, invalid amounts, timestamps)
- Double spending detection (counter reuse, non-incrementing counters)

### ISO20022Service Tests
- ISO20022 message conversion (successful, validation errors)
- Settlement processing (successful, invalid requests)
- Pacs.008 message creation
- Pacs.002 status report creation
- XML conversion and marshaling

### ValidationHelper Tests
- Struct validation (valid, invalid fields, email format)
- Error response generation (with/without validation errors)
- Response structure validation

## Running Tests

### Run All Tests
```bash
make test
# or
./run_tests.sh
```

### Run Service Tests Only
```bash
make test-services
# or
go test -v -race ./internal/services/...
```

### Run Tests with Coverage
```bash
make test-coverage
# or
go test -v -race -coverprofile=coverage.out ./internal/services/...
go tool cover -html=coverage.out -o coverage.html
```

### Run Individual Service Tests
```bash
go test -v ./internal/services/ -run TestAuthService
go test -v ./internal/services/ -run TestCardProvisioningService
go test -v ./internal/services/ -run TestTransactionService
```

### Clean Test Artifacts
```bash
make test-clean
```

## Test Dependencies

The tests use the following testing libraries:

- `github.com/stretchr/testify/assert` - Assertions
- `github.com/stretchr/testify/mock` - Mocking
- `github.com/DATA-DOG/go-sqlmock` - Database mocking
- `github.com/go-redis/redismock/v8` - Redis mocking

## Mock Objects

### MockHSM
Mocks the HSM interface for testing HSM-related functionality:
- GenerateKey
- GetPublicKey
- Sign/Verify
- Encrypt/Decrypt

### Database Mocks
Uses sqlmock to mock database operations:
- Query expectations
- Exec expectations
- Transaction handling

### Redis Mocks
Uses redismock to mock Redis operations:
- Queue operations
- Cache operations

## Test Data

Tests use minimal test data focusing on:
- Valid/invalid request structures
- Edge cases (empty fields, invalid formats)
- Error conditions (database errors, validation failures)
- Success scenarios with expected responses

## Best Practices

1. **Minimal Test Data**: Tests use only the data necessary to validate functionality
2. **Mock External Dependencies**: Database, Redis, and HSM are mocked
3. **Test Both Success and Failure Cases**: Each test covers positive and negative scenarios
4. **Isolated Tests**: Each test is independent and can run in any order
5. **Clear Test Names**: Test names describe the scenario being tested
6. **Assertions**: Use clear assertions to validate expected behavior

## Coverage Goals

The tests aim for high coverage of:
- Business logic validation
- Error handling
- HTTP request/response handling
- Database operations
- External service integrations

## Running in CI/CD

Tests can be integrated into CI/CD pipelines:

```yaml
# Example GitHub Actions step
- name: Run Tests
  run: |
    go test -v -race -coverprofile=coverage.out ./internal/services/...
    go tool cover -func=coverage.out
```

## Troubleshooting

### Common Issues

1. **Import Path Errors**: Ensure all imports use the correct module path
2. **Mock Expectations**: Verify mock expectations match actual calls
3. **Database Schema**: Ensure test queries match actual database schema
4. **Environment Variables**: Some tests may require specific environment setup

### Debug Tips

1. Use `go test -v` for verbose output
2. Add debug prints in tests if needed
3. Check mock expectations are being met
4. Verify test data matches validation rules






nfc_payments_user
P36AmcBBgLSw7TrVNKDrJUu0TbQrFqKN
postgresql://nfc_payments_user:P36AmcBBgLSw7TrVNKDrJUu0TbQrFqKN@dpg-d5iaod6r433s73c81vog-a/nfc_payments

redis://red-d5iaph15pdvs73btmgh0:6379