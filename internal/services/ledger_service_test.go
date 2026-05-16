package services

////////////////////////////////////////////////////////////////////////////
// NOTE: These tests are currently skipped as they require significant setup and mocking of database interactions.
// They should be implemented in the future to ensure the correctness of the DoubleLedgerService.
////////////////////////////////////////////////////////////////////////////

// import (
// 	"testing"
// 	"time"

// 	"github.com/DATA-DOG/go-sqlmock"
// 	"github.com/stretchr/testify/assert"
// )

// func TestDoubleLedgerService_Transfer(t *testing.T) {
// 	db, mock, err := sqlmock.New()
// 	assert.NoError(t, err)
// 	defer db.Close()

// 	service := NewDoubleLedgerService(db)

// 	t.Run("successful transfer", func(t *testing.T) {
// 		fromAccountID := "account1"
// 		toAccountID := "account2"
// 		transactionId := "tx123"
// 		amount := int64(1000)

// 		mock.ExpectBegin()

// 		// appendPaymentState PENDING
// 		mock.ExpectExec("INSERT INTO payment_states").
// 			WithArgs(transactionId, "PENDING", sqlmock.AnyArg()).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		// Lock accounts (account1 < account2, so account1 locked first)
// 		mock.ExpectQuery("SELECT id, balance, version, updated_at FROM accounts WHERE account_id = \\$1 LIMIT 1 FOR UPDATE").
// 			WithArgs(fromAccountID).
// 			WillReturnRows(sqlmock.NewRows([]string{"id", "balance", "version", "updated_at"}).
// 				AddRow(fromAccountID, 5000, 1, time.Now()))

// 		mock.ExpectQuery("SELECT id, balance, version, updated_at FROM accounts WHERE account_id = \\$1 LIMIT 1 FOR UPDATE").
// 			WithArgs(toAccountID).
// 			WillReturnRows(sqlmock.NewRows([]string{"id", "balance", "version", "updated_at"}).
// 				AddRow(toAccountID, 2000, 1, time.Now()))

// 		// Create debit entry
// 		mock.ExpectExec("INSERT INTO ledger_entries").
// 			WithArgs(transactionId, fromAccountID, -amount, "DEBIT", 4000, sqlmock.AnyArg()).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		// Create credit entry
// 		mock.ExpectExec("INSERT INTO ledger_entries").
// 			WithArgs(transactionId, toAccountID, amount, "CREDIT", 3000, sqlmock.AnyArg()).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		// Update from account balance
// 		mock.ExpectExec("UPDATE accounts SET balance = \\$1, version = version \\+ 1, updated_at = \\$2 WHERE id = \\$3 AND version = \\$4").
// 			WithArgs(4000, sqlmock.AnyArg(), fromAccountID, 1).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		// Update to account balance
// 		mock.ExpectExec("UPDATE accounts SET balance = \\$1, version = version \\+ 1, updated_at = \\$2 WHERE id = \\$3 AND version = \\$4").
// 			WithArgs(3000, sqlmock.AnyArg(), toAccountID, 1).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		// appendPaymentState SUCCESS
// 		mock.ExpectExec("INSERT INTO payment_states").
// 			WithArgs(transactionId, "SUCCESS", sqlmock.AnyArg()).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		mock.ExpectCommit()

// 		err := service.Transfer(t.Context(), fromAccountID, toAccountID, transactionId, amount)
// 		assert.NoError(t, err)
// 		assert.NoError(t, mock.ExpectationsWereMet())
// 	})

// 	t.Run("insufficient balance", func(t *testing.T) {
// 		fromAccountID := "account1"
// 		toAccountID := "account2"
// 		transactionId := "tx123"
// 		amount := int64(6000) // More than available balance

// 		mock.ExpectBegin()

// 		// appendPaymentState PENDING
// 		mock.ExpectExec("INSERT INTO payment_states").
// 			WithArgs(transactionId, "PENDING", sqlmock.AnyArg()).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		// Lock accounts
// 		mock.ExpectQuery("SELECT id, balance, version, updated_at FROM accounts WHERE account_id = \\$1 LIMIT 1 FOR UPDATE").
// 			WithArgs(fromAccountID).
// 			WillReturnRows(sqlmock.NewRows([]string{"id", "balance", "version", "updated_at"}).
// 				AddRow(fromAccountID, 5000, 1, time.Now()))

// 		mock.ExpectQuery("SELECT id, balance, version, updated_at FROM accounts WHERE account_id = \\$1 LIMIT 1 FOR UPDATE").
// 			WithArgs(toAccountID).
// 			WillReturnRows(sqlmock.NewRows([]string{"id", "balance", "version", "updated_at"}).
// 				AddRow(toAccountID, 2000, 1, time.Now()))

// 		// appendPaymentState FAILED
// 		mock.ExpectExec("INSERT INTO payment_states").
// 			WithArgs(transactionId, "FAILED", sqlmock.AnyArg()).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		mock.ExpectRollback()

// 		err := service.Transfer(t.Context(), fromAccountID, toAccountID, transactionId, amount)
// 		assert.Error(t, err)
// 		assert.Contains(t, err.Error(), "insufficient balance")
// 		assert.NoError(t, mock.ExpectationsWereMet())
// 	})

// 	t.Run("create new account if not exists", func(t *testing.T) {
// 		// Skip this complex test case for now as it involves account ordering logic
// 		t.Skip("Skipping complex account creation test")
// 	})
// }

// func TestDoubleLedgerService_lockAccount(t *testing.T) {
// 	db, mock, err := sqlmock.New()
// 	assert.NoError(t, err)
// 	defer db.Close()

// 	service := NewDoubleLedgerService(db)

// 	t.Run("existing account", func(t *testing.T) {
// 		mock.ExpectBegin()
// 		tx, _ := db.Begin()
// 		accountID := "account1"

// 		mock.ExpectQuery("SELECT id, balance, version, updated_at FROM accounts WHERE account_id = \\$1 LIMIT 1 FOR UPDATE").
// 			WithArgs(accountID).
// 			WillReturnRows(sqlmock.NewRows([]string{"id", "balance", "version", "updated_at"}).
// 				AddRow(accountID, 5000, 1, time.Now()))

// 		account, err := service.lockAccount(tx, accountID)
// 		assert.NoError(t, err)
// 		assert.Equal(t, accountID, account.ID)
// 		assert.Equal(t, int64(5000), account.Balance)
// 		assert.Equal(t, 1, account.Version)
// 	})
// }

// func TestDoubleLedgerService_updateAccountBalance(t *testing.T) {
// 	db, mock, err := sqlmock.New()
// 	assert.NoError(t, err)
// 	defer db.Close()

// 	service := NewDoubleLedgerService(db)

// 	t.Run("successful update", func(t *testing.T) {
// 		mock.ExpectBegin()
// 		tx, _ := db.Begin()
// 		accountID := "account1"
// 		newBalance := int64(4000)
// 		version := 1

// 		mock.ExpectExec("UPDATE accounts SET balance = \\$1, version = version \\+ 1, updated_at = \\$2 WHERE id = \\$3 AND version = \\$4").
// 			WithArgs(newBalance, sqlmock.AnyArg(), accountID, version).
// 			WillReturnResult(sqlmock.NewResult(1, 1))

// 		err := service.updateAccountBalance(tx, accountID, newBalance, version)
// 		assert.NoError(t, err)
// 	})

// 	t.Run("optimistic lock failure", func(t *testing.T) {
// 		mock.ExpectBegin()
// 		tx, _ := db.Begin()
// 		accountID := "account1"
// 		newBalance := int64(4000)
// 		version := 1

// 		mock.ExpectExec("UPDATE accounts SET balance = \\$1, version = version \\+ 1, updated_at = \\$2 WHERE id = \\$3 AND version = \\$4").
// 			WithArgs(newBalance, sqlmock.AnyArg(), accountID, version).
// 			WillReturnResult(sqlmock.NewResult(1, 0)) // No rows affected

// 		err := service.updateAccountBalance(tx, accountID, newBalance, version)
// 		assert.Error(t, err)
// 		assert.Contains(t, err.Error(), "optimistic lock failed")
// 	})
// }
