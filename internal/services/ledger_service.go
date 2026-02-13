package services

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/ruralpay/backend/internal/models"
)

type DoubleLedgerService struct {
	db              *sql.DB
	systemFeeAccount string
}

func NewDoubleLedgerService(db *sql.DB) *DoubleLedgerService {
	systemFeeAccount := "0000000001"
	if envAccount := os.Getenv("SYSTEM_FEE_ACCOUNT"); envAccount != "" {
		systemFeeAccount = envAccount
	}
	return &DoubleLedgerService{
		db:              db,
		systemFeeAccount: systemFeeAccount,
	}
}

func (s *DoubleLedgerService) Transfer(fromAccountID, toAccountID, transactionID string, amount int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.appendPaymentState(tx, transactionID, "PENDING"); err != nil {
		return err
	}

	if err := s.TransferTx(tx, fromAccountID, toAccountID, transactionID, amount); err != nil {
		s.appendPaymentState(tx, transactionID, "FAILED")
		return err
	}

	if err := s.appendPaymentState(tx, transactionID, "SUCCESS"); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *DoubleLedgerService) TransferTx(tx *sql.Tx, fromAccountID, toAccountID, transactionID string, amount int64) error {
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

	if err := s.createLedgerEntry(tx, transactionID, fromAccount.ID, -amount, "DEBIT", fromAccount.Balance-amount); err != nil {
		return err
	}

	if err := s.createLedgerEntry(tx, transactionID, toAccount.ID, amount, "CREDIT", toAccount.Balance+amount); err != nil {
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

func (s *DoubleLedgerService) appendPaymentState(tx *sql.Tx, transactionID, state string) error {
	_, err := tx.Exec(`
		INSERT INTO payment_states (transaction_id, state, created_at)
		VALUES ($1, $2, $3)`,
		transactionID, state, time.Now())
	return err
}

func (s *DoubleLedgerService) lockAccount(tx *sql.Tx, accountID string) (*models.Account, error) {
	var account models.Account
	err := tx.QueryRow(`
		SELECT id, balance, version, updated_at 
		FROM accounts 
		WHERE account_id = $1 OR card_id = $1
		LIMIT 1
		FOR UPDATE`, accountID).Scan(&account.ID, &account.Balance, &account.Version, &account.UpdatedAt)
	
	return &account, err
}

func (s *DoubleLedgerService) createLedgerEntry(tx *sql.Tx, transactionID, accountID string, amount int64, entryType string, balance int64) error {
	_, err := tx.Exec(`
		INSERT INTO ledger_entries (transaction_id, account_id, amount, entry_type, balance, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		transactionID, accountID, amount, entryType, balance, time.Now())
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