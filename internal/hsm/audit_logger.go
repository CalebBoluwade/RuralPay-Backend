package hsm

import (
	"encoding/json"
	"log"
	"time"
)

type AuditEvent struct {
	Timestamp     time.Time `json:"timestamp"`
	EventType     string    `json:"event_type"`
	TransactionID string    `json:"transaction_id"`
	FromAccountID string    `json:"from_account_id"`
	ToAccountID   string    `json:"to_account_id"`
	Amount        int64     `json:"amount"`
	Status        string    `json:"status"`
	Details       any       `json:"details"`
}

type AuditLogger struct{}

func NewAuditLogger() *AuditLogger {
	return &AuditLogger{}
}

func (a *AuditLogger) LogTransfer(transactionId, fromAccount, toAccount string, amount int64, status string) {
	event := AuditEvent{
		Timestamp:     time.Now(),
		EventType:     "TRANSFER",
		TransactionID: transactionId,
		Amount:        amount,
		Status:        status,
		Details: map[string]string{
			"from_account": fromAccount,
			"to_account":   toAccount,
		},
	}
	a.log(event)
}

func (a *AuditLogger) LogError(transactionId, accountID string, err error) {
	event := AuditEvent{
		Timestamp:     time.Now(),
		EventType:     "ERROR",
		TransactionID: transactionId,
		FromAccountID: accountID,
		Status:        "FAILED",
		Details:       map[string]string{"error": err.Error()},
	}
	a.log(event)
}

func (a *AuditLogger) LogOperation(transactionId, accountID, operation, details string) {
	event := AuditEvent{
		Timestamp:     time.Now(),
		EventType:     operation,
		TransactionID: transactionId,
		FromAccountID: accountID,
		Status:        "SUCCESS",
		Details:       map[string]string{"details": details},
	}
	a.log(event)
}

func (a *AuditLogger) log(event AuditEvent) {
	data, _ := json.Marshal(event)
	log.Printf("AUDIT: %s", string(data))
}
