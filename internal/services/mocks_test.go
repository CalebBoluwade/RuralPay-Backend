package services

import (
	"github.com/ruralpay/backend/internal/hsm"
	"github.com/stretchr/testify/mock"
)

type MockHSM struct {
	mock.Mock
}

func (m *MockHSM) GenerateKeyPair(keyID string) (*hsm.KeyPair, error) {
	args := m.Called(keyID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*hsm.KeyPair), args.Error(1)
}

func (m *MockHSM) GetPublicKey(keyID string) (string, error) {
	args := m.Called(keyID)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) DeleteKey(keyID string) error {
	args := m.Called(keyID)
	return args.Error(0)
}

func (m *MockHSM) RotateKeys() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockHSM) EncryptData(keyID string, plaintext []byte) ([]byte, error) {
	args := m.Called(keyID, plaintext)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockHSM) DecryptData(keyID string, ciphertext []byte) ([]byte, error) {
	args := m.Called(keyID, ciphertext)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
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

func (m *MockHSM) GenerateCardSignature(cardData *hsm.CardData) (string, error) {
	args := m.Called(cardData)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) VerifyCardSignature(cardData *hsm.CardData, signature string) (bool, error) {
	args := m.Called(cardData, signature)
	return args.Bool(0), args.Error(1)
}

func (m *MockHSM) EncryptCardData(cardData *hsm.CardData) (string, error) {
	args := m.Called(cardData)
	return args.String(0), args.Error(1)
}

func (m *MockHSM) DecryptCardData(encryptedData string) (*hsm.CardData, error) {
	args := m.Called(encryptedData)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*hsm.CardData), args.Error(1)
}

func (m *MockHSM) GenerateTransactionID() string {
	args := m.Called()
	return args.String(0)
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

type MockAuditLogger struct {
	mock.Mock
}

func (m *MockAuditLogger) LogTransfer(txID, fromAccount, toAccount string, amount int64, status string) {
	m.Called(txID, fromAccount, toAccount, amount, status)
}

func (m *MockAuditLogger) LogError(txID, cardID string, err error) {
	m.Called(txID, cardID, err)
}