package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/ruralpay/backend/internal/services"
	"github.com/ruralpay/backend/internal/utils"
)

type FeedbackHandler struct {
	db                  *sql.DB
	NotificationService *services.NotificationService
}

func NewFeedbackHandler(db *sql.DB, notificationService *services.NotificationService) *FeedbackHandler {
	return &FeedbackHandler{db: db, NotificationService: notificationService}
}

// HandleTransactionRating records a thumbs-up/down on a payment notification email.
// GET /api/v1/feedback?transaction_id=...&email=...&rating=yes|no
func (h *FeedbackHandler) HandleTransactionRating(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	transactionID := q.Get("transaction_id")
	rating := q.Get("rating")
	email := q.Get("email")

	if transactionID == "" || rating == "" {
		utils.SendErrorResponse(w, "transaction_id and rating are required", http.StatusBadRequest, nil)
		return
	}
	if rating != "yes" && rating != "no" {
		utils.SendErrorResponse(w, "rating must be yes or no", http.StatusBadRequest, nil)
		return
	}

	_, err := h.db.Exec(
		`INSERT INTO transaction_feedback (transaction_id, email, rating, created_at)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (transaction_id, email) DO UPDATE SET rating = EXCLUDED.rating`,
		transactionID, email, rating,
	)
	if err != nil {
		slog.Error("feedback.transaction_rating.db_failed", "transaction_id", transactionID, "error", err)
		utils.SendErrorResponse(w, "Failed to record feedback", http.StatusInternalServerError, nil)
		return
	}

	slog.Info("feedback.transaction_rating.recorded", "transaction_id", transactionID, "rating", rating)

	if email != "" {
		go h.NotificationService.SendFeedbackReceivedEmail(email)
	}

	utils.SendSuccessResponse(w, "Thank you for your feedback!", nil, http.StatusOK)
}

// HandleReferralSource records how a newly registered user heard about RuralPay.
// GET /api/v1/feedback/referral?source=friend|social|search|other&uid=...
func (h *FeedbackHandler) HandleReferralSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	uid := q.Get("uid")

	allowed := map[string]bool{"friend": true, "social": true, "search": true, "other": true}
	if !allowed[source] {
		utils.SendErrorResponse(w, "invalid source", http.StatusBadRequest, nil)
		return
	}

	_, err := h.db.Exec(
		`INSERT INTO user_email_feedback (user_id, type, value, created_at)
		 VALUES (NULLIF($1, '')::INTEGER, 'referral', $2, NOW())
		 ON CONFLICT DO NOTHING`,
		uid, source,
	)
	if err != nil {
		slog.Error("feedback.referral.db_failed", "uid", uid, "source", source, "error", err)
		utils.SendErrorResponse(w, "Failed to record referral source", http.StatusInternalServerError, nil)
		return
	}

	slog.Info("feedback.referral.recorded", "uid", uid, "source", source)
	utils.SendSuccessResponse(w, "Thanks for letting us know!", nil, http.StatusOK)
}

// HandleDeletionReason records why a user deleted their account.
// GET /api/v1/feedback/deletion-reason?reason=too_expensive|switching_app|missing_features|bad_experience|other&uid=...
func (h *FeedbackHandler) HandleDeletionReason(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	reason := q.Get("reason")
	uid := q.Get("uid")

	allowed := map[string]bool{
		"too_expensive":    true,
		"switching_app":    true,
		"missing_features": true,
		"bad_experience":   true,
		"other":            true,
	}
	if !allowed[reason] {
		utils.SendErrorResponse(w, "invalid reason", http.StatusBadRequest, nil)
		return
	}

	_, err := h.db.Exec(
		`INSERT INTO user_email_feedback (user_id, type, value, created_at)
		 VALUES (NULLIF($1, '')::INTEGER, 'deletion', $2, NOW())`,
		uid, reason,
	)
	if err != nil {
		slog.Error("feedback.deletion_reason.db_failed", "uid", uid, "reason", reason, "error", err)
		utils.SendErrorResponse(w, "Failed to record deletion reason", http.StatusInternalServerError, nil)
		return
	}

	slog.Info("feedback.deletion_reason.recorded", "uid", uid, "reason", reason)
	utils.SendSuccessResponse(w, "Thank you for your feedback. We hope to see you again!", nil, http.StatusOK)
}

// HandleConfirmLogin acknowledges a login confirmation click from the email alert.
// GET /api/v1/feedback/confirm-login?confirm=yes&uid=...
func (h *FeedbackHandler) HandleConfirmLogin(w http.ResponseWriter, r *http.Request) {
	confirm := r.URL.Query().Get("confirm")
	uid := r.URL.Query().Get("uid")

	if confirm != "yes" {
		utils.SendErrorResponse(w, "invalid confirmation value", http.StatusBadRequest, nil)
		return
	}

	slog.Info("feedback.login_confirmed", "uid", uid)
	utils.SendSuccessResponse(w, "Login confirmed. You're all set!", nil, http.StatusOK)
}
