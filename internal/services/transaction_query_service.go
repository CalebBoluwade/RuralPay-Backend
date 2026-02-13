package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ruralpay/backend/internal/models"
)

type TransactionQueryService struct {
	db        *sql.DB
	validator *ValidationHelper
}

type TransactionHistory struct {
	TxID        string             `json:"transactionID"`
	FromAccount string             `json:"fromAccount"`
	ToAccount   string             `json:"toAccount"`
	MerchantID  string             `json:"merchantId"`
	Amount      int64              `json:"amount"`
	Currency    string             `json:"currency"`
	PaymentMode models.PaymentMode `json:"paymentMode"`
	Fee         int64              `json:"fee"`
	TxType      string             `json:"txType"`
	Status      string             `json:"status"`
	Narration   string             `json:"narration"`
	CreatedAt   time.Time          `json:"transactionDate"`
}

func NewTransactionQueryService(db *sql.DB) *TransactionQueryService {
	return &TransactionQueryService{
		db:        db,
		validator: NewValidationHelper(),
	}
}

// GetTransaction retrieves a specific transaction
// @Summary Get transaction by ID
// @Description Retrieve a transaction by its ID
// @Tags transactions
// @Produce json
// @Param txId path string true "Transaction ID"
// @Success 200 {object} TransactionDTO
// @Failure 404 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /transactions/{txId} [get]
// @Security BearerAuth
func (s *TransactionQueryService) GetTransaction(w http.ResponseWriter, r *http.Request) {
	txID := chi.URLParam(r, "txId")
	log.Printf("[TRANSACTION] Fetching transaction: %s", txID)

	tx, err := s.fetchTransaction(txID)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("[TRANSACTION] Transaction not found: %s", txID)
			SendErrorResponse(w, "Transaction not found", http.StatusNotFound, nil)
		} else {
			log.Printf("[TRANSACTION] Failed to fetch transaction %s: %v", txID, err)
			SendErrorResponse(w, "Failed to fetch transaction", http.StatusInternalServerError, nil)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tx)
}

// ListTransactions retrieves transactions with optional filters
// @Summary List transactions
// @Description Get a list of transactions with optional filtering
// @Tags transactions
// @Produce json
// @Param id query string false "Filter by transaction ID"
// @Param cardId query string false "Filter by card ID"
// @Param status query string false "Filter by status"
// @Param startDate query string false "Filter by start date (RFC3339 format)"
// @Param endDate query string false "Filter by end date (RFC3339 format)"
// @Success 200 {object} object{transactions=[]TransactionDTO,count=int}
// @Failure 500 {object} services.ErrorResponse
// @Router /transactions [get]
// @Security BearerAuth
func (s *TransactionQueryService) ListTransactions(w http.ResponseWriter, r *http.Request) {
	if txID := r.URL.Query().Get("id"); txID != "" {
		log.Printf("[TRANSACTION] Listing transaction by ID: %s", txID)
		tx, err := s.fetchTransaction(txID)
		if err != nil {
			if err == sql.ErrNoRows {
				log.Printf("[TRANSACTION] Transaction not found: %s", txID)
				SendErrorResponse(w, "Transaction not found", http.StatusNotFound, nil)
			} else {
				log.Printf("[TRANSACTION] Failed to fetch transaction %s: %v", txID, err)
				SendErrorResponse(w, "Failed to fetch transaction", http.StatusInternalServerError, nil)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tx)
		return
	}

	cardID := r.URL.Query().Get("cardId")
	status := r.URL.Query().Get("status")
	startDate := r.URL.Query().Get("startDate")
	endDate := r.URL.Query().Get("endDate")
	limit := 50
	log.Printf("[TRANSACTION] Listing transactions - cardID: %s, status: %s, limit: %d", cardID, status, limit)

	transactions, err := s.fetchTransactions(cardID, status, startDate, endDate, limit)
	if err != nil {
		log.Printf("[TRANSACTION] Failed to fetch transactions: %v", err)
		SendErrorResponse(w, "Failed to fetch transactions", http.StatusInternalServerError, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"transactions": transactions,
		"count":        len(transactions),
	})
}

// GetRecentTransactions retrieves recent transactions
// @Summary Get recent transactions
// @Description Get a list of recent transactions for the authenticated user
// @Tags transactions
// @Produce json
// @Param limit query int false "Number of transactions to return (default: 10, max: 100)"
// @Success 200 {array} TransactionDTO
// @Failure 400 {object} services.ErrorResponse
// @Failure 401 {object} services.ErrorResponse
// @Failure 500 {object} services.ErrorResponse
// @Router /transactions/recent [get]
// @Security BearerAuth
func (s *TransactionQueryService) GetRecentTransactions(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		log.Println("[TRANSACTION] Unauthorized access to recent transactions")
		SendErrorResponse(w, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}

	var req struct {
		Limit int `validate:"omitempty,min=1,max=100"`
	}
	req.Limit = 10

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			req.Limit = l
		}
	}

	if err := s.validator.ValidateStruct(&req); err != nil {
		log.Printf("[TRANSACTION] Validation failed for recent transactions: %v", err)
		SendErrorResponse(w, "Validation failed", http.StatusBadRequest, err)
		return
	}

	log.Printf("[TRANSACTION] Fetching recent transactions for user %s, limit: %d", userID, req.Limit)
	transactions, err := s.fetchRecentTransactions(userID, req.Limit)
	if err != nil {
		SendErrorResponse(w, "Failed to fetch recent transactions", http.StatusInternalServerError, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transactions)
}

func (s *TransactionQueryService) fetchTransaction(txID string) (*TransactionHistory, error) {
	log.Printf("[TRANSACTION] Querying database for transaction: %s", txID)
	tx := &TransactionHistory{}
	var amountStr, feeStr string
	err := s.db.QueryRow(`
		SELECT transaction_id, from_card_id, to_card_id, amount::text, currency, 
		       COALESCE(type, 'DEBIT') as type, COALESCE(payment_mode, 'CARD') as payment_mode, fee::text, status, COALESCE(narration, ''), created_at
		FROM transactions
		WHERE transaction_id = $1
	`, txID).Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt)

	if err != nil {
		log.Printf("[TRANSACTION] Database error for transaction %s: %v", txID, err)
		return nil, err
	}

	amount, _ := strconv.ParseFloat(amountStr, 64)
	tx.Amount = int64(amount)
	fee, _ := strconv.ParseFloat(feeStr, 64)
	tx.Fee = int64(fee)
	log.Printf("[TRANSACTION] Successfully fetched transaction %s", txID)
	return tx, nil
}

func (s *TransactionQueryService) fetchTransactions(cardID, status, startDate, endDate string, limit int) ([]TransactionHistory, error) {
	var conditions []string
	var args []any
	argIndex := 1

	baseQuery := `
		SELECT transaction_id, from_card_id, to_card_id, amount, currency, 
		       COALESCE(type, 'DEBIT') as type, COALESCE(payment_mode, 'CARD') as payment_mode, fee, status, COALESCE(narration, ''), created_at
		FROM transactions
	`

	if cardID != "" {
		conditions = append(conditions, fmt.Sprintf("(from_card_id = $%d OR to_card_id = $%d)", argIndex, argIndex))
		args = append(args, cardID)
		argIndex++
	}

	if status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIndex))
		args = append(args, status)
		argIndex++
	}

	if startDate != "" {
		if parsedDate, err := time.Parse(time.RFC3339, startDate); err == nil {
			conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIndex))
			args = append(args, parsedDate)
			argIndex++
		}
	}

	if endDate != "" {
		if parsedDate, err := time.Parse(time.RFC3339, endDate); err == nil {
			conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIndex))
			args = append(args, parsedDate)
			argIndex++
		}
	}

	query := baseQuery
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d", argIndex)
	args = append(args, limit)

	log.Printf("[TRANSACTION] Executing query with %d conditions", len(conditions))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		log.Printf("[TRANSACTION] Query failed: %v", err)
		return nil, err
	}
	defer rows.Close()

	var transactions []TransactionHistory
	for rows.Next() {
		tx := TransactionHistory{}
		err := rows.Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &tx.Amount, &tx.Currency, &tx.TxType, &tx.PaymentMode, &tx.Fee, &tx.Status, &tx.Narration, &tx.CreatedAt)
		if err != nil {
			log.Printf("[TRANSACTION] Failed to scan row: %v", err)
			return nil, err
		}
		transactions = append(transactions, tx)
	}

	log.Printf("[TRANSACTION] Fetched %d transactions", len(transactions))
	return transactions, nil
}

func (s *TransactionQueryService) fetchRecentTransactions(userID string, limit int) ([]TransactionHistory, error) {
	query := `
		SELECT transaction_id, from_card_id, to_card_id, amount::text, currency, 
		       COALESCE(type, 'DEBIT') as type, COALESCE(payment_mode, 'CARD') as payment_mode, fee::text, status, COALESCE(narration, ''), created_at
		FROM transactions
		WHERE user_id = $1::integer
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := s.db.Query(query, userID, limit)
	if err != nil {
		log.Printf("[TRANSACTION] Failed to query recent transactions for user %s: %v", userID, err)
		return nil, err
	}
	defer rows.Close()

	var transactions []TransactionHistory
	for rows.Next() {
		tx := TransactionHistory{}
		var amountStr, feeStr string
		err := rows.Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt)
		if err != nil {
			log.Printf("[TRANSACTION] Failed to scan row for user %s: %v", userID, err)
			return nil, err
		}
		amount, _ := strconv.ParseFloat(amountStr, 64)
		tx.Amount = int64(amount)
		fee, _ := strconv.ParseFloat(feeStr, 64)
		tx.Fee = int64(fee)
		transactions = append(transactions, tx)
	}

	log.Printf("[TRANSACTION] Fetched %d recent transactions for user %s", len(transactions), userID)
	return transactions, nil
}
