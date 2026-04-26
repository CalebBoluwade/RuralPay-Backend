package services

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestAuthService_Register(t *testing.T) {
	db, mock, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	// Setup viper config
	viper.Set("argon2.salt_length", 16)
	viper.Set("argon2.time", 1)
	viper.Set("argon2.memory", 64*1024)
	viper.Set("argon2.threads", 4)
	viper.Set("argon2.key_length", 32)
	viper.Set("jwt.secret_key", "test-secret")
	viper.Set("jwt.expiry_minutes", 10)

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	service := NewUserService(db, redisClient, nil, nil)

	t.Run("successful registration", func(t *testing.T) {
		req := models.RegisterRequest{
			Email:         "test@example.com",
			Password:      "password123",
			FirstName:     "John",
			LastName:      "Doe",
			Username:      "johndoe",
			BVN:           "12345678901",
			PhoneNumber:   "+2348012345678",
			IdentityToken: "TOKEN1234567",
		}

		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO users").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		mock.ExpectCommit()

		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.RegisterNewUser(w, r)

		assert.Equal(t, http.StatusOK, w.Code)
		var response utils.APISuccessResponse
		json.Unmarshal(w.Body.Bytes(), &response)
		assert.True(t, response.Success)
	})

	t.Run("Unable To Process This Request At This Time", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer([]byte("invalid")))
		w := httptest.NewRecorder()

		service.RegisterNewUser(w, r)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAuthService_Login(t *testing.T) {
	db, mock, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	viper.Set("jwt.secret_key", "test-secret")
	viper.Set("jwt.expiry_minutes", 10)
	viper.Set("argon2.salt_length", 16)
	viper.Set("argon2.time", 1)
	viper.Set("argon2.memory", 64*1024)
	viper.Set("argon2.threads", 4)
	viper.Set("argon2.key_length", 32)
	viper.Set("auth.use_encrypted_password", false)

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	service := NewUserService(db, redisClient, nil, nil)

	t.Run("successful login", func(t *testing.T) {
		hashedPassword, _ := hashPassword("password123")

		mock.ExpectQuery("SELECT.*FROM users u").
			WithArgs("4359502429542").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "first_name", "last_name", "phone_number", "bvn", "username", "password", "account_id", "m.id", "m.business_name", "m.business_type", "m.tax_id", "m.status", "m.commission_rate", "m.settlement_cycle", "m.created_at", "m.updated_at"}).
				AddRow(1, "test@example.com", "John", "Doe", "+2348012345678", "12345678901", "johndoe", hashedPassword, "4359502429542", nil, nil, nil, nil, nil, nil, nil, nil, nil))

		mock.ExpectQuery("SELECT").
			WithArgs("1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "is_admin"}).AddRow("active", false))

		req := models.LoginRequest{
			Identifier: "4359502429542",
			Password:   "password123",
			DeviceInfo: models.DeviceInfo{
				Platform:  "iOS",
				Model:     "iPhone 14",
				OSVersion: "16.0",
			},
		}

		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.UserLogin(w, r)

		assert.Equal(t, http.StatusOK, w.Code)
		var response utils.APISuccessResponse
		json.Unmarshal(w.Body.Bytes(), &response)
		details, ok := response.Details.(map[string]any)
		assert.True(t, ok)
		assert.NotEmpty(t, details["token"])
	})

	t.Run("user not found", func(t *testing.T) {
		mock.ExpectQuery("SELECT.*FROM users u").
			WithArgs("34324920424942").
			WillReturnError(sql.ErrNoRows)

		req := models.LoginRequest{
			Identifier: "34324920424942",
			Password:   "password123",
			DeviceInfo: models.DeviceInfo{
				Platform:  "Android",
				Model:     "Pixel 6",
				OSVersion: "13",
			},
		}

		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.UserLogin(w, r)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestPasswordHashing(t *testing.T) {
	viper.Set("argon2.salt_length", 16)
	viper.Set("argon2.time", 1)
	viper.Set("argon2.memory", 64*1024)
	viper.Set("argon2.threads", 4)
	viper.Set("argon2.key_length", 32)

	password := "testpassword"

	hashed, err := hashPassword(password)
	assert.NoError(t, err)
	assert.NotEmpty(t, hashed)

	assert.True(t, verifyPassword(password, hashed))
	assert.False(t, verifyPassword("wrongpassword", hashed))
}

func TestGenerateJWT(t *testing.T) {
	viper.Set("jwt.secret_key", "test-secret")
	viper.Set("jwt.expiry_minutes", 10)

	db, mock, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	svc := NewUserService(db, redisClient, nil, nil)

	merchantID := 1
	merchantStatus := "active"

	mock.ExpectQuery("SELECT").
		WithArgs("123").
		WillReturnRows(sqlmock.NewRows([]string{"status", "is_admin"}).AddRow("active", false))

	token, err := svc.generateJWTWithSession(123, models.Merchant{ID: merchantID, Status: merchantStatus}, "test-session-id", "test-device-id")
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
}
