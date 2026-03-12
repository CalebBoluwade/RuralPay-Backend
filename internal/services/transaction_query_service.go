package services

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
)

type TransactionQueryService struct {
	db        *sql.DB
	validator *ValidationHelper
}

type TransactionHistory struct {
	TxID           string             `json:"transactionId"`
	FromAccount    string             `json:"fromAccount"`
	ToAccount      string             `json:"toAccount"`
	MerchantID     string             `json:"merchantId,omitempty"`
	Amount         int64              `json:"amount"`
	Currency       string             `json:"currency"`
	PaymentMode    models.PaymentMode `json:"paymentMode"`
	Fee            int64              `json:"fee"`
	TxType         string             `json:"txType"`
	Status         string             `json:"status"`
	Narration      string             `json:"narration"`
	CreatedAt      time.Time          `json:"transactionDate"`
	Profit         *float64           `json:"profit,omitempty"`
	SettlementDate *time.Time         `json:"settlementDate,omitempty"`
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

	tx, err := s.fetchTransaction(txID)
	if err != nil {
		if err == sql.ErrNoRows {
			utils.SendErrorResponse(w, "Transaction Not Found", http.StatusNotFound, nil)
		} else {
			slog.Error("transaction.get.failed", "tx_id", txID, "error", err)
			utils.SendErrorResponse(w, "Failed To Fetch Transaction", http.StatusFailedDependency, nil)
		}
		return
	}

	utils.SendSuccessResponse(w, "Transaction Fetched Successfully", tx, http.StatusOK)
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
		tx, err := s.fetchTransaction(txID)
		if err != nil {
			if err == sql.ErrNoRows {
				utils.SendErrorResponse(w, "Transaction not found", http.StatusNotFound, nil)
			} else {
				slog.Error("transaction.list.fetch_failed", "tx_id", txID, "error", err)
				utils.SendErrorResponse(w, "Failed to fetch transaction", http.StatusFailedDependency, nil)
			}
			return
		}

		utils.SendSuccessResponse(w, "Transaction fetched successfully", tx, http.StatusOK)

		return
	}

	cardID := r.URL.Query().Get("cardId")
	status := r.URL.Query().Get("status")
	startDate := r.URL.Query().Get("startDate")
	endDate := r.URL.Query().Get("endDate")
	limit := 50

	transactions, err := s.fetchTransactions(cardID, status, startDate, endDate, limit)
	if err != nil {
		slog.Error("transaction.list.query_failed", "error", err)
		utils.SendErrorResponse(w, "Failed to fetch transactions", http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "recent transactions fetched successfully", transactions, http.StatusOK)
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
	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

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
		slog.Warn("transaction.recent.validation_failed", "error", err)
		utils.SendErrorResponse(w, utils.ValidationError, http.StatusBadRequest, err)
		return
	}

	var transactions []TransactionHistory
	var err error
	if merchantID == 0 {
		transactions, err = s.fetchRecentUserTransactions((userID), req.Limit)
	} else {
		transactions, err = s.fetchRecentMerchantTransactions((merchantID), req.Limit)
	}

	if err != nil {
		utils.SendErrorResponse(w, "Failed to fetch recent transactions", http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "recent transactions fetched successfully", transactions, http.StatusOK)
}

func (s *TransactionQueryService) fetchTransaction(txID string) (*TransactionHistory, error) {
	tx := &TransactionHistory{}
	var amountStr, feeStr string
	err := s.db.QueryRow(`
		SELECT transaction_id, debit_id, credit_id, amount::text, currency, 
		       COALESCE(type, 'DEBIT') as type, COALESCE(payment_mode, 'CARD') as payment_mode, fee::text, status, COALESCE(narration, ''), created_at
		FROM transactions
		WHERE transaction_id = $1
	`, txID).Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt)

	if err != nil {
		slog.Error("transaction.fetch.db_error", "tx_id", txID, "error", err)
		return nil, err
	}

	amount, _ := strconv.ParseFloat(amountStr, 64)
	tx.Amount = int64(amount)
	fee, _ := strconv.ParseFloat(feeStr, 64)
	tx.Fee = int64(fee)
	return tx, nil
}

func (s *TransactionQueryService) fetchTransactions(cardID, status, startDate, endDate string, limit int) ([]TransactionHistory, error) {
	var conditions []string
	var args []any
	argIndex := 1

	baseQuery := `
		SELECT transaction_id, debit_id, credit_id, amount, currency, 
		       COALESCE(type, 'DEBIT') as type, COALESCE(payment_mode, 'CARD') as payment_mode, fee, status, COALESCE(narration, ''), created_at
		FROM transactions
	`

	if cardID != "" {
		conditions = append(conditions, fmt.Sprintf("(debit_id = $%d OR credit_id = $%d)", argIndex, argIndex))
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

	rows, err := s.db.Query(query, args...)
	if err != nil {
		slog.Error("transaction.fetch_list.query_failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	var transactions []TransactionHistory
	for rows.Next() {
		tx := TransactionHistory{}
		err := rows.Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &tx.Amount, &tx.Currency, &tx.TxType, &tx.PaymentMode, &tx.Fee, &tx.Status, &tx.Narration, &tx.CreatedAt)
		if err != nil {
			slog.Error("transaction.fetch_list.scan_failed", "error", err)
			return nil, err
		}
		transactions = append(transactions, tx)
	}

	return transactions, nil
}

func (s *TransactionQueryService) fetchRecentUserTransactions(userID int, limit int) ([]TransactionHistory, error) {
	query := `
		SELECT transaction_id, COALESCE(debit_id, '') as debit_id, COALESCE(credit_id, '') as credit_id, amount::text, currency, 
		       COALESCE(type, 'DEBIT') as type, COALESCE(payment_mode, 'CARD') as payment_mode, fee::text, status, COALESCE(narration, ''), created_at
		FROM transactions
		WHERE user_id = $1::integer
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := s.db.Query(query, userID, limit)
	if err != nil {
		slog.Error("transaction.fetch_user_recent.query_failed", "user_id", userID, "error", err)
		return nil, err
	}
	defer rows.Close()

	var transactions []TransactionHistory
	for rows.Next() {
		tx := TransactionHistory{}
		var amountStr, feeStr string
		err := rows.Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt)
		if err != nil {
			slog.Error("transaction.fetch_user_recent.scan_failed", "user_id", userID, "error", err)
			return nil, err
		}
		amount, _ := strconv.ParseFloat(amountStr, 64)
		tx.Amount = int64(amount)
		fee, _ := strconv.ParseFloat(feeStr, 64)
		tx.Fee = int64(fee)
		transactions = append(transactions, tx)
	}

	return transactions, nil
}

func (s *TransactionQueryService) fetchRecentMerchantTransactions(merchantID int, limit int) ([]TransactionHistory, error) {
	query := `
		SELECT
			t.transaction_id,
			COALESCE(t.debit_id, '') AS debit_id,
			COALESCE(t.credit_id, '') AS credit_id,
			t.amount::text,
			t.currency,
			COALESCE(t.type, 'DEBIT') AS type,
			COALESCE(t.payment_mode, 'CARD') AS payment_mode,
			t.fee::text,
			t.status,
			COALESCE(t.narration, ''),
			t.created_at,
			(t.amount * m.commission_rate / 100)::text AS profit,
			CASE m.settlement_cycle
				WHEN 'DAILY'   THEN t.created_at + INTERVAL '1 day'
				WHEN 'WEEKLY'  THEN t.created_at + INTERVAL '7 days'
				WHEN 'MONTHLY' THEN t.created_at + INTERVAL '1 month'
			END AS settlement_date
		FROM transactions t
		JOIN merchants m ON m.account_id = t.credit_id
		WHERE m.id = $1::integer
		ORDER BY t.created_at DESC
		LIMIT $2
	`

	rows, err := s.db.Query(query, merchantID, limit)
	if err != nil {
		slog.Error("transaction.fetch_merchant_recent.query_failed", "merchant_id", merchantID, "error", err)
		return nil, err
	}
	defer rows.Close()

	transactions := []TransactionHistory{}
	for rows.Next() {
		tx := TransactionHistory{}
		var amountStr, feeStr, profitStr string
		var settlementDate sql.NullTime
		err := rows.Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt, &profitStr, &settlementDate)
		if err != nil {
			slog.Error("transaction.fetch_merchant_recent.scan_failed", "merchant_id", merchantID, "error", err)
			return nil, err
		}
		amount, _ := strconv.ParseFloat(amountStr, 64)
		tx.Amount = int64(amount)
		fee, _ := strconv.ParseFloat(feeStr, 64)
		tx.Fee = int64(fee)
		if profit, err := strconv.ParseFloat(profitStr, 64); err == nil {
			tx.Profit = &profit
		}
		if settlementDate.Valid {
			tx.SettlementDate = &settlementDate.Time
		}
		transactions = append(transactions, tx)
	}

	if err := rows.Err(); err != nil {
		slog.Error("transaction.fetch_merchant_recent.rows_error", "merchant_id", merchantID, "error", err)
		return nil, err
	}
	return transactions, nil
}
