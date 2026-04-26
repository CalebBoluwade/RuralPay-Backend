package services

import (
	"crypto/rsa"

	"github.com/ruralpay/backend/internal/hsm"
	"github.com/stretchr/testify/mock"
)

type MockHSM struct {
	mock.Mock
}

func (m *MockHSM) GetPublicKey(keyID string) (*rsa.PublicKey, error) {
	args := m.Called(keyID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rsa.PublicKey), args.Error(1)
}

func (m *MockHSM) GetPrivateKey(keyID string) (*rsa.PrivateKey, error) {
	args := m.Called(keyID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rsa.PrivateKey), args.Error(1)
}

func (m *MockHSM) DeleteKey(keyID string) error {
	args := m.Called(keyID)
	return args.Error(0)
}

func (m *MockHSM) RotateKeys() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockHSM) GenerateAndSaveKeyPairExternal(keyID string) (*hsm.KeyPair, error) {
	args := m.Called(keyID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*hsm.KeyPair), args.Error(1)
}

func (m *MockHSM) EncryptData(keyID string, plaintext []byte) ([]byte, error) {
	args := m.Called(keyID, plaintext)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockHSM) DecryptData(keyID string, payload []byte) (string, error) {
	args := m.Called(keyID, payload)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) SignData(keyID string, data []byte) ([]byte, error) {
	args := m.Called(keyID, data)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockHSM) VerifySignature(keyID string, data, signature []byte) (bool, error) {
	args := m.Called(keyID, data, signature)
	return args.Bool(0), args.Error(1)
}

func (m *MockHSM) SignTransaction(transaction *hsm.Transaction) (string, error) {
	args := m.Called(transaction)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) VerifyTransaction(transaction *hsm.Transaction, signature string) (bool, error) {
	args := m.Called(transaction, signature)
	return args.Bool(0), args.Error(1)
}

func (m *MockHSM) HashPIN(pin string, salt []byte) (string, error) {
	args := m.Called(pin, salt)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) VerifyPIN(pin string, hashedPIN string) (bool, error) {
	args := m.Called(pin, hashedPIN)
	return args.Bool(0), args.Error(1)
}

func (m *MockHSM) DecryptPII(encryptedData string) (string, error) {
	args := m.Called(encryptedData)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) EncryptPAN(pan string) (string, error) {
	args := m.Called(pan)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) DecryptPAN(encrypted string) (string, error) {
	args := m.Called(encrypted)
	return args.String(0), args.Error(1)
}

type MockAuditLogger struct {
	mock.Mock
}

func (m *MockAuditLogger) LogTransfer(txID, fromAccount, toAccount string, amount int64, status string) {
	m.Called(txID, fromAccount, toAccount, amount, status)
}

func (m *MockAuditLogger) LogError(txID, cardID string, err error) {
	m.Called(txID, cardID, err)
}