package services

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
)

func TestCardService_ProvisionCard(t *testing.T) {
	db, _, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	mockHSM := &MockHSM{}
	service := NewCardService(db, mockHSM)

	t.Run("successful ", func(t *testing.T) {
		req := ProvisionRequest{
			UserID:         1,
			CardType:       "DEBIT",
			InitialBalance: 100.0,
		}

		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/cards/provision", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.ProvisionCard(w, r)

		assert.Equal(t, http.StatusOK, w.Code)
		var response map[string]string
		json.Unmarshal(w.Body.Bytes(), &response)
		assert.Equal(t, "provisioned", response["status"])
	})

	t.Run("invalid card type", func(t *testing.T) {
		req := ProvisionRequest{
			UserID:         1,
			CardType:       "INVALID",
			InitialBalance: 100.0,
		}

		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/cards/provision", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.ProvisionCard(w, r)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("Unable To Process This Request At This Time", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/cards/provision", bytes.NewBuffer([]byte("invalid")))
		w := httptest.NewRecorder()

		service.ProvisionCard(w, r)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestCardService_ActivateCard(t *testing.T) {
	db, _, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	mockHSM := &MockHSM{}
	service := NewCardService(db, mockHSM)

	t.Run("successful activation", func(t *testing.T) {
		req := ActivationRequest{
			CardID:         "card123",
			ActivationCode: "123456",
		}

		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/cards/activate", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.ActivateCard(w, r)

		assert.Equal(t, http.StatusOK, w.Code)
		var response map[string]string
		json.Unmarshal(w.Body.Bytes(), &response)
		assert.Equal(t, "activated", response["status"])
	})

	t.Run("invalid activation code length", func(t *testing.T) {
		req := ActivationRequest{
			CardID:         "card123",
			ActivationCode: "123",
		}

		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/cards/activate", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		service.ActivateCard(w, r)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestCardService_GetCard(t *testing.T) {
	db, _, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	mockHSM := &MockHSM{}
	service := NewCardService(db, mockHSM)

	r := chi.NewRouter()
	r.Get("/cards/{cardId}", service.GetCard)

	req := httptest.NewRequest("GET", "/cards/card123", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var response map[string]string
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.Equal(t, "card123", response["cardId"])
	assert.Equal(t, "active", response["status"])
}

func TestCardService_SuspendCard(t *testing.T) {
	db, _, err := sqlmock.New()
	assert.NoError(t, err)
	defer db.Close()

	mockHSM := &MockHSM{}
	service := NewCardService(db, mockHSM)

	r := chi.NewRouter()
	r.Put("/cards/{cardId}/suspend", service.SuspendCard)

	req := httptest.NewRequest("PUT", "/cards/card123/suspend", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var response map[string]string
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.Equal(t, "card123", response["cardId"])
	assert.Equal(t, "suspended", response["status"])
}
