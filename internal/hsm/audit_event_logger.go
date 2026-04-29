package hsm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/ruralpay/backend/internal/models"
)

type AuditLogger struct {
	db  *sql.DB
	HSM HSMInterface
}

func NewAuditLogger(db *sql.DB, hsmInstance HSMInterface) *AuditLogger {
	return &AuditLogger{
		db:  db,
		HSM: hsmInstance,
	}
}

func (audit *AuditLogger) LogTransaction(ctx context.Context, tx *sql.Tx, event models.AuditEvent) error {
	slog.Info("audit.process.inserting", "tx_id", event.TxRequest.TransactionID)

	// Generate HSM Signature For The Audit Transaction Event
	timestamp := time.Now()
	nonce := uuid.New().String()

	hsmTx := &Transaction{
		ID:            event.TxRequest.TransactionID,
		FromAccountID: event.TxRequest.FromAccount,
		ToAccountID:   event.TxRequest.BeneficiaryAccountNumber,
		Amount:        float64(event.TxRequest.Amount),
		Timestamp:     timestamp,
		Nonce:         nonce,
	}
	signature, err := audit.HSM.SignTransaction(hsmTx)
	if err != nil {
		slog.Error("card.process.sign_failed", "tx_id", event.TxRequest.TransactionID, "error", err)
		return errors.New("security signing failed")
	}

	// Update metadata with signing info
	if event.Details == nil {
		event.Details = make(map[string]any)
	}
	event.Details["signing_nonce"] = nonce
	event.Details["signing_timestamp"] = timestamp
	metadataJSON, _ := json.Marshal(event.Details)

	var debitId string

	if event.TxRequest.PaymentMode == "CARD" {
		encryptedPAN, err := audit.HSM.EncryptPAN(event.TxRequest.FromAccount)
		if err != nil {
			slog.Error("card.process.pan_encrypt_failed", "tx_id", event.TxRequest.TransactionID, "error", err)
			return errors.New("failed to Secure Card Data")
		}
		debitId = encryptedPAN
	} else {
		debitId = event.TxRequest.FromAccount
	}

	_, err = tx.ExecContext(ctx, `
		WITH merchant AS (
			SELECT account_id FROM merchants WHERE id = $8
		)
		INSERT INTO transactions
			(transaction_id, debit_id, credit_id, amount, total_amount, type, payment_mode, status, user_id, created_at)
		SELECT $1, $2,
			CASE WHEN $5 = 'CARD' THEN (SELECT account_id FROM merchant) ELSE $3 END,
			$4, $4, 'DEBIT', $5, $6, $7, NOW()
	`,
		event.TxRequest.TransactionID,            // $1
		debitId,                                  // $2
		event.TxRequest.BeneficiaryAccountNumber, // $3
		event.TxRequest.Amount,                   // $4
		event.TxRequest.PaymentMode,              // $5
		models.TransactionStatusPending,          // $6
		event.TxRequest.UserID,                   // $7
		event.Details["merchantID"],              // $8
	)
	if err != nil {
		slog.Error("audit.process.tx_insert_failed", "tx_id", event.TxRequest.TransactionID, "error", err)
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events
			(transaction_id, amount, longitude, latitude, ip_address, t2factor_used, signature, metadata, event_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
	`,
		event.TxRequest.TransactionID,      // $1
		event.TxRequest.Amount,             // $2
		event.TxRequest.Location.Longitude, // $3
		event.TxRequest.Location.Latitude,  // $4
		event.TxRequest.IPAddress,          // $5
		event.TxRequest.TwoFAType,          // $6
		string(signature),                  // $7
		metadataJSON,                       // $8
		event.EventType,                    // $9
	)
	if err != nil {
		slog.Error("audit.process.insert_failed", "tx_id", event.TxRequest.TransactionID, "error", err)
		return err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("audit.process.commit_failed", "tx_id", event.TxRequest.TransactionID, "error", err)
		return err
	}

	audit.log(event)

	return nil
}

func (audit *AuditLogger) LogFailedTransaction(ctx context.Context, event models.AuditEvent) error {
	slog.Info("audit.process.failed_transaction", "tx_id", event.TxRequest.TransactionID)

	timestamp := time.Now()
	nonce := uuid.New().String()

	hsmTx := &Transaction{
		ID:            event.TxRequest.TransactionID,
		FromAccountID: event.TxRequest.FromAccount,
		ToAccountID:   event.TxRequest.BeneficiaryAccountNumber,
		Amount:        float64(event.TxRequest.Amount),
		Timestamp:     timestamp,
		Nonce:         nonce,
	}
	signature, err := audit.HSM.SignTransaction(hsmTx)
	if err != nil {
		slog.Error("card.process.sign_failed", "tx_id", event.TxRequest.TransactionID, "error", err)
		return errors.New("security signing failed")
	}

	if event.Details == nil {
		event.Details = make(map[string]any)
	}
	event.Details["signing_nonce"] = nonce
	event.Details["signing_timestamp"] = timestamp
	if event.Error != "" {
		event.Details["error"] = event.Error
	}
	metadataJSON, _ := json.Marshal(event.Details)

	_, err = audit.db.ExecContext(ctx, `
		INSERT INTO audit_events
			(transaction_id, amount, longitude, latitude, ip_address, t2factor_used, signature, metadata, event_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
	`,
		event.TxRequest.TransactionID,      // $1
		event.TxRequest.Amount,             // $2
		event.TxRequest.Location.Longitude, // $3
		event.TxRequest.Location.Latitude,  // $4
		event.TxRequest.IPAddress,          // $5
		event.TxRequest.TwoFAType,          // $6
		string(signature),                  // $7
		metadataJSON,                       // $8
		event.EventType,                    // $9
	)
	if err != nil {
		slog.Error("audit.process.failed_insert", "transactionId", event.TxRequest.TransactionID, "error", err)
		return err
	}

	audit.log(event)
	return nil
}

func (audit *AuditLogger) LogActivity(ctx context.Context, event models.ActivityEvent) error {
	if event.Details == nil {
		event.Details = make(map[string]any)
	}
	if event.Error != "" {
		event.Details["error"] = event.Error
	}
	metadataJSON, _ := json.Marshal(event.Details)

	_, err := audit.db.ExecContext(ctx, `
		INSERT INTO audit_events
			(ip_address, metadata, event_type, created_at)
		VALUES ($1, $2, $3, NOW())
	`, event.IPAddress, metadataJSON, event.EventType)
	if err != nil {
		slog.Error("audit.activity.insert_failed", "event_type", event.EventType, "user_id", event.UserID, "error", err)
		return err
	}

	data, _ := json.Marshal(event)
	slog.Debug(fmt.Sprintf("[AUDIT]: %s", string(data)))
	return nil
}


// 	event := AuditEvent{
// 		Timestamp:     time.Now(),
// 		EventType:     operation,
// 		TransactionID: transactionId,
// 		FromAccountID: accountID,
// 		Status:        "SUCCESS",
// 		Details:       map[string]any{"details": details},
// 	}
// 	a.log(event)
// }

func (a *AuditLogger) log(event models.AuditEvent) {
	data, _ := json.Marshal(event)
	slog.Debug(fmt.Sprintf("[AUDIT]: %s", string(data)))
}
