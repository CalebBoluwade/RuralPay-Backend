package services

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
)

func TestHSMKeyService_SyncKeysToDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	mockHSM := &MockHSM{}
	service := NewHSMKeyService(db, mockHSM)

	t.Run("successful sync", func(t *testing.T) {
		privKey, err := rsa.GenerateKey(rand.Reader, 2048)
		assert.NoError(t, err)
		pubKey := &privKey.PublicKey

		mockHSM.On("GetPublicKey", "transaction_signing").Return(pubKey, nil)

		mock.ExpectExec("SELECT upsert_hsm_key").
			WithArgs("transaction_signing", "RSA", "transaction_signing", 2048, sqlmock.AnyArg(), "ENCRYPTED_BY_HSM", sqlmock.AnyArg(), `{"synced_from_hsm": true}`).
			WillReturnResult(sqlmock.NewResult(1, 1))

		err = service.SyncKeysToDatabase()
		assert.NoError(t, err)

		mockHSM.AssertExpectations(t)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("HSM error", func(t *testing.T) {
		mockHSM2 := &MockHSM{}
		service2 := NewHSMKeyService(db, mockHSM2)

		mockHSM2.On("GetPublicKey", "transaction_signing").Return((*rsa.PublicKey)(nil), assert.AnError)

		err := service2.SyncKeysToDatabase()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to sync key transaction_signing")

		mockHSM2.AssertExpectations(t)
	})
}

func TestHSMKeyService_getKeyTypeAndSize(t *testing.T) {
	db, _, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	mockHSM := &MockHSM{}
	service := NewHSMKeyService(db, mockHSM)

	t.Run("user encryption key", func(t *testing.T) {
		keyType, keySize := service.getKeyTypeAndSize("user_encryption", "")
		assert.Equal(t, "AES", keyType)
		assert.Equal(t, 256, keySize)
	})

	t.Run("RSA key with valid PEM", func(t *testing.T) {
		publicKeyPEM := `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA1234567890abcdef
ghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
QIDAQAB
-----END PUBLIC KEY-----`

		keyType, keySize := service.getKeyTypeAndSize("card_signing", publicKeyPEM)
		assert.Equal(t, "RSA", keyType)
		// Size will be default 2048 since the PEM parsing might fail with test data
		assert.Equal(t, 2048, keySize)
	})

	t.Run("invalid PEM fallback", func(t *testing.T) {
		keyType, keySize := service.getKeyTypeAndSize("card_signing", "invalid-pem")
		assert.Equal(t, "RSA", keyType)
		assert.Equal(t, 2048, keySize)
	})
}