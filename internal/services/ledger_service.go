package services

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/ruralpay/backend/internal/models"
)

type DoubleLedgerService struct {
	db               *sql.DB
	systemFeeAccount string
}

func NewDoubleLedgerService(db *sql.DB) *DoubleLedgerService {
	systemFeeAccount := "0000000001"
	if envAccount := os.Getenv("SYSTEM_FEE_ACCOUNT"); envAccount != "" {
		systemFeeAccount = envAccount
	}
	return &DoubleLedgerService{
		db:               db,
		systemFeeAccount: systemFeeAccount,
	}
}

func (s *DoubleLedgerService) Transfer(fromAccountID, toAccountID, transactionId string, amount int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.appendPaymentState(tx, transactionId, "PENDING"); err != nil {
		return err
	}

	if err := s.TransferTx(tx, fromAccountID, toAccountID, transactionId, amount); err != nil {
		s.appendPaymentState(tx, transactionId, "FAILED")
		return err
	}

	if err := s.appendPaymentState(tx, transactionId, "SUCCESS"); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *DoubleLedgerService) Reverse(transactionId string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT account_id, amount, entry_type, balance 
		FROM ledger_entries 
		WHERE transaction_id = $1 
		ORDER BY created_at`, transactionId)
	if err != nil {
		return err
	}
	defer rows.Close()

	type entry struct {
		accountID string
		amount    int64
		entryType string
		balance   int64
	}
	var entries []entry

	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.accountID, &e.amount, &e.entryType, &e.balance); err != nil {
			return err
		}
		entries = append(entries, e)
	}

	if len(entries) == 0 {
		return fmt.Errorf("[Ledger] No Entries Found for transaction %s", transactionId)
	}

	for _, e := range entries {
		account, err := s.lockAccount(tx, e.accountID)
		if err != nil {
			return err
		}

		reversalAmount := -e.amount
		reversalType := "CREDIT"
		if e.entryType == "CREDIT" {
			reversalType = "DEBIT"
		}
		newBalance := account.Balance + reversalAmount

		if err := s.createLedgerEntry(tx, transactionId+"_REVERSAL", e.accountID, reversalAmount, reversalType, newBalance); err != nil {
			return err
		}

		if err := s.updateAccountBalance(tx, account.ID, newBalance, account.Version); err != nil {
			return err
		}
	}

	if err := s.appendPaymentState(tx, transactionId, "REVERSED"); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *DoubleLedgerService) TransferTx(tx *sql.Tx, fromAccountID, toAccountID, transactionId string, amount int64) error {
	// Lock accounts in consistent order to prevent deadlocks
	firstLock, secondLock := fromAccountID, toAccountID
	if fromAccountID > toAccountID {
		firstLock, secondLock = toAccountID, fromAccountID
	}

	fromAccount, err := s.lockAccount(tx, firstLock)
	if err != nil {
		return err
	}

	toAccount, err := s.lockAccount(tx, secondLock)
	if err != nil {
		return err
	}

	// Determine which locked account is sender/receiver
	if firstLock != fromAccountID {
		fromAccount, toAccount = toAccount, fromAccount
	}

	if fromAccount.Balance < amount {
		return fmt.Errorf("insufficient balance")
	}

	if err := s.createLedgerEntry(tx, transactionId, fromAccount.ID, -amount, "DEBIT", fromAccount.Balance-amount); err != nil {
		return err
	}

	if err := s.createLedgerEntry(tx, transactionId, toAccount.ID, amount, "CREDIT", toAccount.Balance+amount); err != nil {
		return err
	}

	if err := s.updateAccountBalance(tx, fromAccount.ID, fromAccount.Balance-amount, fromAccount.Version); err != nil {
		return err
	}

	if err := s.updateAccountBalance(tx, toAccount.ID, toAccount.Balance+amount, toAccount.Version); err != nil {
		return err
	}

	return nil
}

func (s *DoubleLedgerService) appendPaymentState(tx *sql.Tx, transactionId, state string) error {
	_, err := tx.Exec(`
		INSERT INTO payment_states (transaction_id, state, created_at)
		VALUES ($1, $2, $3)`,
		transactionId, state, time.Now())
	return err
}

func (s *DoubleLedgerService) lockAccount(tx *sql.Tx, accountID string) (*models.Account, error) {
	var account models.Account
	err := tx.QueryRow(`
		SELECT id, balance, version, updated_at 
		FROM accounts 
		WHERE account_id = $1
		LIMIT 1
		FOR UPDATE`, accountID).Scan(&account.ID, &account.Balance, &account.Version, &account.UpdatedAt)

	return &account, err
}

func (s *DoubleLedgerService) createLedgerEntry(tx *sql.Tx, transactionId, accountID string, amount int64, entryType string, balance int64) error {
	_, err := tx.Exec(`
		INSERT INTO ledger_entries (transaction_id, account_id, amount, entry_type, balance, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		transactionId, accountID, amount, entryType, balance, time.Now())
	return err
}

func (s *DoubleLedgerService) updateAccountBalance(tx *sql.Tx, accountID string, newBalance int64, version int) error {
	result, err := tx.Exec(`
		UPDATE accounts 
		SET balance = $1, version = version + 1, updated_at = $2 
		WHERE id = $3 AND version = $4`,
		newBalance, time.Now(), accountID, version)

	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return fmt.Errorf("optimistic lock failed for account %s", accountID)
	}

	return nil
}
