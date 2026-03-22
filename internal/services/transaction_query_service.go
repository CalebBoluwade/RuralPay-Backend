package services

import (
	"database/sql"
	"errors"
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

const (
	defaultPageLimit = 20
	maxPageLimit     = 100
)

type txFilters struct {
	Status    string
	StartDate string
	EndDate   string
}

type PaginatedTransactions struct {
	Transactions []TransactionHistory `json:"transactions"`
	Total        int                  `json:"total"`
	Page         int                  `json:"page"`
	Limit        int                  `json:"limit"`
	HasMore      bool                 `json:"hasMore"`
}

func parsePagination(r *http.Request) (page, limit int) {
	page = 1
	limit = defaultPageLimit
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		if l > maxPageLimit {
			l = maxPageLimit
		}
		limit = l
	}
	return
}

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
	Fee            int64              `json:"fee,omitempty"`
	TxType         string             `json:"txType"`
	Status         string             `json:"status"`
	Narration      string             `json:"narration,omitempty"`
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

func (s *TransactionQueryService) fetchTransaction(txID string, userID int) (*TransactionHistory, error) {
	tx := &TransactionHistory{}
	var amountStr, feeStr string
	err := s.db.QueryRow(`
		SELECT transaction_id, COALESCE(debit_id, ''), COALESCE(credit_id, ''), amount::text, currency,
		       COALESCE(type, 'DEBIT'), COALESCE(payment_mode, 'CARD'), fee::text, status, COALESCE(narration, ''), created_at
		FROM transactions
		WHERE transaction_id = $1 AND user_id = $2
	`, txID, userID).Scan(&tx.TxID, &tx.FromAccount, &tx.MerchantID, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("transaction.fetch.db_error", "tx_id", txID, "error", err)
		}
		return nil, err
	}
	amount, _ := strconv.ParseFloat(amountStr, 64)
	tx.Amount = int64(amount)
	fee, _ := strconv.ParseFloat(feeStr, 64)
	tx.Fee = int64(fee)
	return tx, nil
}

func (s *TransactionQueryService) fetchMerchantTransaction(txID string, merchantID int) (*TransactionHistory, error) {
	tx := &TransactionHistory{}
	var amountStr, feeStr, profitStr string
	var settlementDate sql.NullTime
	err := s.db.QueryRow(`
		SELECT t.transaction_id, COALESCE(t.debit_id, ''), COALESCE(t.credit_id, ''), t.amount::text, t.currency,
		       COALESCE(t.type, 'CREDIT'), COALESCE(t.payment_mode, 'CARD'), t.fee::text, t.status, COALESCE(t.narration, ''), t.created_at,
		       (t.amount * m.commission_rate / 100)::text,
		       CASE m.settlement_cycle
		           WHEN 'DAILY'   THEN t.created_at + INTERVAL '1 day'
		           WHEN 'WEEKLY'  THEN t.created_at + INTERVAL '7 days'
		           WHEN 'MONTHLY' THEN t.created_at + INTERVAL '1 month'
		       END
		FROM transactions t
		JOIN merchants m ON m.account_id = t.credit_id
		WHERE t.transaction_id = $1 AND m.id = $2
	`, txID, merchantID).Scan(&tx.TxID, &tx.FromAccount, &tx.ToAccount, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt, &profitStr, &settlementDate)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("transaction.fetch_merchant.db_error", "tx_id", txID, "error", err)
		}
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
	return tx, nil
}

// GetTransaction retrieves a specific transaction
// @Summary Get transaction by ID
// @Description Retrieve a transaction by its ID
// @Tags Transactions
// @Produce json
// @Param txId path string true "Transaction ID"
// @Success 200 {object} TransactionHistory
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /transaction/{txId} [get]
func (s *TransactionQueryService) GetTransaction(w http.ResponseWriter, r *http.Request) {
	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())
	txID := chi.URLParam(r, "txId")

	var tx *TransactionHistory
	var err error
	if merchantID != 0 {
		tx, err = s.fetchMerchantTransaction(txID, merchantID)
	} else {
		tx, err = s.fetchTransaction(txID, userID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			utils.SendErrorResponse(w, "Transaction Not Found", http.StatusNotFound, nil)
		} else {
			slog.Error("transaction.get.failed", "tx_id", txID, "error", err)
			utils.SendErrorResponse(w, "Failed To Fetch Transaction", http.StatusFailedDependency, nil)
		}
		return
	}

	utils.SendSuccessResponse(w, "Transaction Fetched Successfully", tx, http.StatusOK)
}

// GetRecentTransactions retrieves recent transactions with optional filters
// @Summary Get Transactions
// @Description Get a list of recent transactions for the authenticated user with optional filtering
// @Tags Transactions
// @Produce json
// @Param status query string false "Filter by status"
// @Param startDate query string false "Filter by start date (RFC3339 format)"
// @Param endDate query string false "Filter by end date (RFC3339 format)"
// @Param page query int false "Page Number"
// @Param limit query int false "Page Size, Number of transactions to return (default: 10, max: 100)"
// @Success 200 {array} PaginatedTransactions
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /transaction/recent [get]
func (s *TransactionQueryService) GetRecentTransactions(w http.ResponseWriter, r *http.Request) {
	userID, merchantID := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	page, limit := parsePagination(r)
	filters := txFilters{
		Status:    r.URL.Query().Get("status"),
		StartDate: r.URL.Query().Get("startDate"),
		EndDate:   r.URL.Query().Get("endDate"),
	}

	var results *PaginatedTransactions
	var err error
	if merchantID == 0 {
		results, err = s.fetchRecentUserTransactions(userID, filters, page, limit)
	} else {
		results, err = s.fetchRecentMerchantTransactions(merchantID, filters, page, limit)
	}
	if err != nil {
		utils.SendErrorResponse(w, "Failed to Fetch Transactions", http.StatusFailedDependency, nil)
		return
	}

	utils.SendSuccessResponse(w, "Transactions Fetched Successfully", results, http.StatusOK)
}

func buildFilterClauses(argIdx int, f txFilters, col string) ([]string, []any) {
	var clauses []string
	var args []any
	if f.Status != "" {
		clauses = append(clauses, fmt.Sprintf("%sstatus = $%d", col, argIdx))
		args = append(args, strings.ToUpper(f.Status))
		argIdx++
	}
	if f.StartDate != "" {
		if t, err := time.Parse(time.RFC3339, f.StartDate); err == nil {
			clauses = append(clauses, fmt.Sprintf("%screated_at >= $%d", col, argIdx))
			args = append(args, t)
			argIdx++
		}
	}
	if f.EndDate != "" {
		if t, err := time.Parse(time.RFC3339, f.EndDate); err == nil {
			clauses = append(clauses, fmt.Sprintf("%screated_at <= $%d", col, argIdx))
			args = append(args, t)
		}
	}
	return clauses, args
}

func (s *TransactionQueryService) fetchRecentUserTransactions(userID int, f txFilters, page, limit int) (*PaginatedTransactions, error) {
	args := []any{userID}
	clauses, filterArgs := buildFilterClauses(2, f, "")
	args = append(args, filterArgs...)
	argIdx := 2 + len(filterArgs)

	offset := (page - 1) * limit
	args = append(args, limit, offset)

	const base = "SELECT transaction_id, COALESCE(debit_id, ''), COALESCE(credit_id, ''), amount::text, currency," +
		" COALESCE(type, 'DEBIT'), COALESCE(payment_mode, 'CARD'), fee::text, status, COALESCE(narration, ''), created_at," +
		" COUNT(*) OVER() AS total_count FROM transactions WHERE user_id = $1"
	query := base + " AND " + strings.Join(clauses, " AND ")
	if len(clauses) == 0 {
		query = base
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)

	rows, err := s.db.Query(query, args...)

	if err != nil {
		slog.Error("transaction.fetch_user_recent.query_failed", "user_id", userID, "error", err)
		return nil, err
	}
	defer rows.Close()

	var total int
	var transactions []TransactionHistory
	for rows.Next() {
		var tx TransactionHistory
		var amountStr, feeStr string
		if err := rows.Scan(&tx.TxID, &tx.FromAccount, &tx.ToAccount, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt, &total); err != nil {
			slog.Error("transaction.fetch_user_recent.scan_failed", "user_id", userID, "error", err)
			return nil, err
		}
		amount, _ := strconv.ParseFloat(amountStr, 64)
		tx.Amount = int64(amount)
		fee, _ := strconv.ParseFloat(feeStr, 64)
		tx.Fee = int64(fee)
		transactions = append(transactions, tx)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if transactions == nil {
		transactions = []TransactionHistory{}
	}
	return &PaginatedTransactions{
		Transactions: transactions,
		Total:        total,
		Page:         page,
		Limit:        limit,
		HasMore:      page*limit < total,
	}, nil
}

func (s *TransactionQueryService) fetchRecentMerchantTransactions(merchantID int, f txFilters, page, limit int) (*PaginatedTransactions, error) {
	args := []any{merchantID}
	clauses, filterArgs := buildFilterClauses(2, f, "t.")
	args = append(args, filterArgs...)
	argIdx := 2 + len(filterArgs)

	offset := (page - 1) * limit
	args = append(args, limit, offset)

	const base = "SELECT t.transaction_id, COALESCE(t.debit_id, ''), COALESCE(t.credit_id, ''), t.amount::text, t.currency," +
		" COALESCE(t.type, 'CREDIT'), COALESCE(t.payment_mode, 'CARD'), t.fee::text, t.status, COALESCE(t.narration, ''), t.created_at," +
		" (t.amount * m.commission_rate / 100)::text," +
		" CASE m.settlement_cycle WHEN 'DAILY' THEN t.created_at + INTERVAL '1 day' WHEN 'WEEKLY' THEN t.created_at + INTERVAL '7 days' WHEN 'MONTHLY' THEN t.created_at + INTERVAL '1 month' END," +
		" COUNT(*) OVER() AS total_count FROM transactions t JOIN merchants m ON m.account_id = t.credit_id WHERE m.id = $1"
	query := base + " AND " + strings.Join(clauses, " AND ")
	if len(clauses) == 0 {
		query = base
	}
	query += fmt.Sprintf(" ORDER BY t.created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		slog.Error("transaction.fetch_merchant_recent.query_failed", "merchant_id", merchantID, "error", err)
		return nil, err
	}
	defer rows.Close()

	var total int
	var transactions []TransactionHistory
	for rows.Next() {
		var tx TransactionHistory
		var amountStr, feeStr, profitStr string
		var settlementDate sql.NullTime
		if err := rows.Scan(&tx.TxID, &tx.FromAccount, &tx.ToAccount, &amountStr, &tx.Currency, &tx.TxType, &tx.PaymentMode, &feeStr, &tx.Status, &tx.Narration, &tx.CreatedAt, &profitStr, &settlementDate, &total); err != nil {
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
	if transactions == nil {
		transactions = []TransactionHistory{}
	}
	return &PaginatedTransactions{
		Transactions: transactions,
		Total:        total,
		Page:         page,
		Limit:        limit,
		HasMore:      page*limit < total,
	}, nil
}
