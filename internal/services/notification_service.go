package services

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	mail "github.com/go-mail/mail/v2"
	goExpo "github.com/montovaneli/go-expo-notification"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/spf13/viper"
)

type NotificationService struct {
	db *sql.DB

	smtpHost     string
	smtpPort     int
	smtpUser     string
	smtpPassword string
	smtpFrom     string
	smtpName     string
	smtpUseSSL   bool
	templatesDir string
	smsURL       string
	httpClient   *http.Client
}

func NewNotificationService(db *sql.DB) *NotificationService {
	return &NotificationService{
		db: db,

		smtpHost:     viper.GetString("smtp.host"),
		smtpPort:     viper.GetInt("smtp.port"),
		smtpUser:     viper.GetString("smtp.user"),
		smtpPassword: viper.GetString("smtp.password"),
		smtpFrom:     viper.GetString("smtp.from"),
		smtpName:     viper.GetString("smtp.name"),
		smtpUseSSL:   viper.GetBool("smtp.ssl"),
		templatesDir: viper.GetString("templates.dir"),
		smsURL:       viper.GetString("sms.url"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetUserNotifications retrieves user notifications for the authenticated user
// @Summary Get User Notifications
// @Description Retrieve all user notifications within a time period for the authenticated user
// @Tags Accounts
// @Produce json
// @Success 200 {array} models.Notification
// @Failure 400 {object} utils.APIErrorResponse
// @Failure 401 {object} utils.APIErrorResponse
// @Security BearerAuth
// @Router /account/notifications [get]
func (ns *NotificationService) GetUserNotifications(w http.ResponseWriter, r *http.Request) {
	slog.Info("get.user.notifications.start")
	ctx := r.Context()
	userID, _ := utils.ExtractUserMerchantInfoFromContext(w, r.Context())

	query := `
        SELECT id, title, type, message, read, created_at
        FROM notifications
        WHERE user_id = $1
        ORDER BY created_at DESC
        LIMIT 50
    `

	rows, err := ns.db.QueryContext(ctx, query, userID)
	if err != nil {
		slog.Error("get.user.notifications.query_failed", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to fetch user notifications", http.StatusFailedDependency, nil)
		return
	}
	defer rows.Close()

	notifications := make([]models.Notification, 0)
	for rows.Next() {
		var n models.Notification
		var createdAt time.Time
		if err := rows.Scan(&n.ID, &n.Title, &n.Type, &n.Message, &n.Read, &createdAt); err != nil {
			slog.Error("account.user.notifications.scan_failed", "user_id", userID, "error", err)
			continue
		}
		n.Time = utils.FormatTime(createdAt)
		notifications = append(notifications, n)
	}

	if err = rows.Err(); err != nil {
		slog.Error("account.user.notifications.rows_error", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, "Failed to process notifications", http.StatusInternalServerError, nil)
		return
	}

	slog.Info("account.GetUserNotifications.success", "user_id", userID, "count", len(notifications))
	utils.SendSuccessResponse(w, utils.ResponseMessage(fmt.Sprintf("%d Notifications Found", len(notifications))), notifications, http.StatusOK)
}

func (ns *NotificationService) SendPaymentNotification(ctx context.Context, transaction *models.TransactionRecord, notifType models.NotificationType) error {
	payload := ns.buildPaymentPayload(4, transaction, notifType)
	return ns.Route(payload)
}

func (ns *NotificationService) Route(payload *models.NotificationPayload) error {
	slog.Info("notification.routing", "user_id", payload.UserID, "title", payload.Title)

	user := ns.getUserPreferences(context.Background(), payload.UserID)

	for _, channel := range user.Notifications.PreferredChannels {
		switch channel {
		case models.ChannelPush:
			if payload.ExpoPushToken != "" {
				slog.Info("notification.dispatch", "channel", "push", "user_id", payload.UserID)
				go ns.sendPush(payload)
			}
		case models.ChannelEmail:
			if payload.Email != "" {
				slog.Info("notification.dispatch", "channel", "email", "email", payload.Email)
				go ns.sendPaymentNotificationEmail(payload)
			}
		case models.ChannelSMS:
			if payload.PhoneNumber != "" {
				slog.Info("notification.dispatch", "channel", "sms", "phone", payload.PhoneNumber, "user_id", payload.UserID)
				go ns.sendSMSNotificationAlert(payload)
			}
		}
	}

	return nil
}

func (ns *NotificationService) sendPush(payload *models.NotificationPayload) error {
	// 1. Create a new Expo Push Client
	client := goExpo.NewPushClient(nil)

	// 2. Define the recipient
	m := &goExpo.PushMessage{
		To:       []goExpo.ExponentPushToken{goExpo.ExponentPushToken(payload.ExpoPushToken)},
		Title:    payload.Title,
		Body:     payload.Body,
		Data:     payload.Data,
		Sound:    "default",
		Priority: "default",
	}

	_, err := client.Publish(m)
	if err != nil {
		slog.Error("notification.push.failed", "user_id", payload.UserID, "error", err)
		return err
	}

	slog.Info("notification.push.sent", "user_id", payload.UserID)
	return nil
}

func (ns *NotificationService) sendPaymentNotificationEmail(payload *models.NotificationPayload) error {
	if ns.smtpHost == "" || ns.smtpUser == "" {
		slog.Warn("notification.email.skipped", "reason", "smtp_not_configured", "user_id", payload.UserID)
		return nil
	}

	body, err := ns.renderEmailTemplate("payment_notification.html", payload)
	if err != nil {
		slog.Error("notification.email.template_failed", "user_id", payload.UserID, "error", err)
		return err
	}

	m := mail.NewMessage()
	m.SetAddressHeader("From", ns.smtpFrom, ns.smtpName)
	m.SetHeader("To", payload.Email)
	m.SetHeader("Subject", payload.Title)
	m.SetBody("text/html", body)

	d := mail.NewDialer(ns.smtpHost, ns.smtpPort, ns.smtpUser, ns.smtpPassword)

	// Handle SSL Correctly
	if ns.smtpUseSSL {
		d.SSL = true
	}
	if err := d.DialAndSend(m); err != nil {
		slog.Error("notification.email.failed", "user_id", payload.UserID, "error", err)
		return err
	}

	slog.Info("notification.email.sent", "user_id", payload.UserID)
	return nil
}

type otpEmailData struct {
	Title       string
	OTP         string
	ExpiresIn   string
	FeedbackURL string
	BaseURL     string
}

func (ns *NotificationService) SendOTPEmail(email, otp, expiresIn string, notifType models.NotificationType) error {
	if ns.smtpHost == "" || ns.smtpUser == "" {
		slog.Warn("email.notification.otp.skipped", "reason", "smtp_not_configured")
		return nil
	}

	body, err := ns.renderTemplate("otp.html", otpEmailData{Title: "Your Verification Code", OTP: otp, ExpiresIn: expiresIn, BaseURL: viper.GetString("app.base_url")})
	if err != nil {
		slog.Error("notification.otp.template_failed", "error", err)
		return err
	}

	var title string

	switch notifType {
	case models.TransactionOTP:
		title = "Your Secure Transaction 2FA Code"
	case models.ForgotPassword:
		title = "Your Forgot Password Verification Code"
	case models.ValidateAccount:
		title = "Verify User Account Code"
	}

	m := mail.NewMessage()
	m.SetAddressHeader("From", ns.smtpFrom, ns.smtpName)
	m.SetHeader("To", email)
	m.SetHeader("Subject", title)
	m.SetBody("text/html", body)

	d := mail.NewDialer(ns.smtpHost, ns.smtpPort, ns.smtpUser, ns.smtpPassword)

	// Handle SSL Correctly
	if ns.smtpUseSSL {
		d.SSL = true
	}

	if err := d.DialAndSend(m); err != nil {
		slog.Error("otp.notification.failed", "error", err)
		return err
	}

	slog.Info("otp.notification.sent", "type", notifType, "email", email)
	return nil
}

func (ns *NotificationService) SendFeedbackReceivedEmail(email string) {
	if ns.smtpHost == "" || ns.smtpUser == "" {
		slog.Warn("feedback.email.notification.skipped", "reason", "smtp_not_configured")
	}

	body, err := ns.renderTemplate("feedback.html", nil)
	if err != nil {
		slog.Error("notification.otp.template_failed", "error", err)
	}

	m := mail.NewMessage()
	m.SetAddressHeader("From", ns.smtpFrom, ns.smtpName)
	m.SetHeader("To", email)
	m.SetHeader("Subject", "Your Feedback Received")
	m.SetBody("text/html", body)

	d := mail.NewDialer(ns.smtpHost, ns.smtpPort, ns.smtpUser, ns.smtpPassword)

	// Handle SSL Correctly
	if ns.smtpUseSSL {
		d.SSL = true
	}
	if err := d.DialAndSend(m); err != nil {
		slog.Error("feedback.notification.failed", "error", err)
	}

	slog.Info("feedback.notification.sent", "email", email)
}

func (ns *NotificationService) SendOTPSmS(phoneNumber, otp, expiresIn string, notifType models.NotificationType) error {
	if ns.smsURL == "" {
		slog.Warn("sms.notification.skipped", "reason", "smtp_not_configured")
		return nil
	}

	body, err := ns.renderTemplate("otp.html", otpEmailData{Title: "Your Verification Code", OTP: otp, ExpiresIn: expiresIn, BaseURL: viper.GetString("app.base_url")})
	if err != nil {
		slog.Error("notification.otp.template_failed", "error", err)
		return err
	}

	var title string

	switch notifType {
	case models.TransactionOTP:
		title = "Your Secure Transaction 2FA Code"
	case models.ForgotPassword:
		title = "Your Forgot Password Verification Code"
	case models.ValidateAccount:
		title = "Verify User Account Code"
	}

	payload := &models.NotificationPayload{
		UserID:      0,
		Title:       title,
		Body:        body,
		PhoneNumber: phoneNumber,
		FeedbackURL: "",
	}

	slog.Info("notification.dispatch", "channel", "sms", "phone", payload.PhoneNumber, "user_id", payload.UserID)
	go ns.sendSMSNotificationAlert(payload)

	slog.Info("notification.otp.sent", "type", notifType, "phone_number", phoneNumber)
	return nil
}

func (ns *NotificationService) renderEmailTemplate(name string, payload *models.NotificationPayload) (string, error) {
	return ns.renderTemplate(name, payload)
}

func (ns *NotificationService) renderTemplate(name string, data any) (string, error) {
	dir := ns.templatesDir
	if dir == "" {
		dir = "./internal/templates/email"
	}

	tmpl, err := template.ParseFiles(filepath.Join(dir, "base.html"), filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		return "", fmt.Errorf("execute template %s: %w", name, err)
	}
	return buf.String(), nil
}

func (ns *NotificationService) sendSMSNotificationAlert(payload *models.NotificationPayload) error {
	if ns.smsURL == "" {
		slog.Warn("notification.sms.skipped", "reason", "sms_url_not_configured")
		return nil
	}

	smsPayload := map[string]any{
		"to":      payload.PhoneNumber,
		"message": fmt.Sprintf("%s\n%s", payload.Title, payload.Body),
	}

	body, _ := json.Marshal(smsPayload)
	req, err := http.NewRequest("POST", ns.smsURL, bytes.NewBuffer(body))
	if err != nil {
		slog.Error("notification.sms.request_failed", "user_id", payload.UserID, "error", err)
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := ns.httpClient.Do(req)
	if err != nil {
		slog.Error("notification.sms.failed", "user_id", payload.UserID, "error", err)
		return err
	}
	defer resp.Body.Close()

	slog.Info("notification.sms.sent", "user_id", payload.UserID, "status", resp.StatusCode)
	return nil
}

func (ns *NotificationService) buildPaymentPayload(userId int, transaction *models.TransactionRecord, notifType models.NotificationType) *models.NotificationPayload {
	var title, body string

	switch notifType {
	case models.PaymentReceived:
		title = "Payment Received"
		body = fmt.Sprintf("You Received %s %.2f From %s", transaction.Currency, float64(transaction.Amount), transaction.OriginatorAccount)
	case models.PaymentSent:
		title = "Payment Sent"
		body = fmt.Sprintf("There Was a Debit Transaction On Your Account of %s %.2f", transaction.Currency, float64(transaction.Amount))
	case models.PaymentFailed:
		title = "Payment Failed"
		body = fmt.Sprintf("Payment of %s %.2f Failed To %s", transaction.Currency, float64(transaction.Amount), fmt.Sprintf("%v", transaction.Metadata["beneficiaryName"]))
	case models.PaymentPending:
		title = "Payment Pending"
		body = fmt.Sprintf("Payment of %s %.2f is Pending", transaction.Currency, float64(transaction.Amount))
	}

	statusClass := map[models.NotificationType]string{
		models.PaymentReceived: "received",
		models.PaymentSent:     "sent",
		models.PaymentFailed:   "failed",
		models.PaymentPending:  "pending",
	}

	slog.Info("notification.payload.building", "transaction_id", transaction.TransactionID, "status", transaction.Status, "type", notifType)
	user := ns.getUserPreferences(context.Background(), userId)

	return &models.NotificationPayload{
		Type:          notifType,
		Title:         title,
		Body:          body,
		Email:         user.Email,
		PhoneNumber:   user.PhoneNumber,
		ExpoPushToken: user.ExpoPushToken,
		FeedbackURL:   fmt.Sprintf("%s/api/v1/feedback?transaction_id=%s&email=%s", viper.GetString("app.base_url"), transaction.TransactionID, user.Email),
		BaseURL:       viper.GetString("app.base_url"),
		Data: map[string]string{
			"transactionId":       transaction.TransactionID,
			"amount":              fmt.Sprintf("%.2f", float64(transaction.Amount)),
			"currency":            transaction.Currency,
			"status":              string(transaction.Status),
			"statusClass":         statusClass[notifType],
			"senderFirstName":     user.FirstName,
			"beneficiaryName":     fmt.Sprintf("%v", transaction.Metadata["beneficiaryName"]),
			"beneficiaryBankName": fmt.Sprintf("%v", transaction.Metadata["beneficiaryBankName"]),
			"beneficiaryAcct":     transaction.BeneficiaryAccount,
			"fee":                 fmt.Sprintf("%.2f", float64(transaction.Fee)),
			"reference":           fmt.Sprintf("%v", transaction.Metadata["reference"]),
			"date":                transaction.CreatedAt.Format("02 Jan 2006, 03:04 PM"),
		},
	}
}

type accountEmailData struct {
	Title       string
	FirstName   string
	LastName    string
	Email       string
	Device      string
	Date        string
	BaseURL     string
	SupportURL  string
	FeedbackURL string
	UserID      int
}

func (ns *NotificationService) SendRegisterEmail(user *models.User) error {
	if ns.smtpHost == "" || ns.smtpUser == "" {
		slog.Warn("notification.email.skipped", "reason", "smtp_not_configured", "user_id", user.ID)
		return nil
	}
	body, err := ns.renderTemplate("register.html", accountEmailData{
		Title:      "Welcome to RuralPay",
		FirstName:  user.FirstName,
		LastName:   user.LastName,
		Email:      user.Email,
		Date:       time.Now().Format("02 Jan 2006, 03:04 PM"),
		BaseURL:    viper.GetString("app.base_url"),
		SupportURL: viper.GetString("app.support_url"),
		UserID:     user.ID,
	})
	if err != nil {
		slog.Error("notification.register.template_failed", "user_id", user.ID, "error", err)
		return err
	}
	m := mail.NewMessage()
	m.SetAddressHeader("From", ns.smtpFrom, ns.smtpName)
	m.SetHeader("To", user.Email)
	m.SetHeader("Subject", "Welcome to RuralPay")
	m.SetBody("text/html", body)

	d := mail.NewDialer(ns.smtpHost, ns.smtpPort, ns.smtpUser, ns.smtpPassword)

	// Handle SSL Correctly
	if ns.smtpUseSSL {
		d.SSL = true
	}

	if err := d.DialAndSend(m); err != nil {
		slog.Error("notification.register.email_failed", "user_id", user.ID, "error", err)
		return err
	}
	slog.Info("notification.register.email_sent", "user_id", user.ID)
	return nil
}

func (ns *NotificationService) SendLoginEmail(user *models.User, device string) error {
	if ns.smtpHost == "" || ns.smtpUser == "" {
		slog.Warn("notification.email.skipped", "reason", "smtp_not_configured", "user_id", user.ID)
		return nil
	}
	body, err := ns.renderTemplate("login.html", accountEmailData{
		Title:      "New Login Detected On Your Account",
		FirstName:  user.FirstName,
		Device:     device,
		Date:       time.Now().Format("02 Jan 2006, 03:04 PM"),
		BaseURL:    viper.GetString("app.base_url"),
		SupportURL: viper.GetString("app.support_url"),
		UserID:     user.ID,
	})
	if err != nil {
		slog.Error("notification.login.template_failed", "user_id", user.ID, "error", err)
		return err
	}
	m := mail.NewMessage()
	m.SetAddressHeader("From", ns.smtpFrom, ns.smtpName)
	m.SetHeader("To", user.Email)
	m.SetHeader("Subject", "RuralPay Account Login")
	m.SetBody("text/html", body)

	d := mail.NewDialer(ns.smtpHost, ns.smtpPort, ns.smtpUser, ns.smtpPassword)

	// Handle SSL Correctly
	if ns.smtpUseSSL {
		d.SSL = true
	}

	if err := d.DialAndSend(m); err != nil {
		slog.Error("notification.login.email_failed", "user_id", user.ID, "error", err)
		return err
	}
	slog.Info("notification.login.email_sent", "user_id", user.ID)
	return nil
}

func (ns *NotificationService) SendDeleteAccountEmail(user *models.User) error {
	if ns.smtpHost == "" || ns.smtpUser == "" {
		slog.Warn("notification.email.skipped", "reason", "smtp_not_configured", "user_id", user.ID)
		return nil
	}
	body, err := ns.renderTemplate("delete_account.html", accountEmailData{
		Title:      "Account Deleted",
		FirstName:  user.FirstName,
		Date:       time.Now().Format("02 Jan 2006, 03:04 PM"),
		BaseURL:    viper.GetString("app.base_url"),
		SupportURL: viper.GetString("app.support_url"),
		UserID:     user.ID,
	})
	if err != nil {
		slog.Error("notification.delete_account.template_failed", "user_id", user.ID, "error", err)
		return err
	}
	m := mail.NewMessage()
	m.SetAddressHeader("From", ns.smtpFrom, ns.smtpName)
	m.SetHeader("To", user.Email)
	m.SetHeader("Subject", "Your RuralPay Account Has Been Deleted")
	m.SetBody("text/html", body)

	d := mail.NewDialer(ns.smtpHost, ns.smtpPort, ns.smtpUser, ns.smtpPassword)

	// Handle SSL Correctly
	if ns.smtpUseSSL {
		d.SSL = true
	}
	if err := d.DialAndSend(m); err != nil {
		slog.Error("notification.delete_account.email_failed", "user_id", user.ID, "error", err)
		return err
	}
	slog.Info("notification.delete_account.email_sent", "user_id", user.ID)
	return nil
}

func (ns *NotificationService) getUserPreferences(ctx context.Context, id int) *models.User {
	user := &models.User{ID: id}
	var pushToken sql.NullString
	var nDevicePush, nSMS, nEmail sql.NullBool

	var channels []models.NotificationChannel

	err := ns.db.QueryRowContext(ctx, `
		SELECT u.email, u.phone_number, u.push_token, u.first_name,
		       n.use_device_push, n.use_sms, n.use_email
		FROM users u
		LEFT JOIN notifications n ON n.user_id = u.id
		WHERE u.id = $1
	`, id).Scan(&user.Email, &user.PhoneNumber, &pushToken, &user.FirstName,
		&nDevicePush, &nSMS, &nEmail)

	if err != nil {
		slog.Error("payment.fetch_user_failed", "user_id", id, "error", err)

		user.Notifications.PreferredChannels = []models.NotificationChannel{models.ChannelEmail}

		return user
	}

	user.ExpoPushToken = pushToken.String
	user.Notifications = models.UserNotifications{
		UserID:     id,
		DevicePush: nDevicePush.Bool,
		SMS:        nSMS.Bool,
		Email:      nEmail.Bool,
	}

	slog.Info("notification.user_preferences.fetched", "user_id", user.ID, "push", user.Notifications.DevicePush, "sms", user.Notifications.SMS, "email", user.Notifications.Email)

	if user.Notifications.DevicePush && user.ExpoPushToken != "" {
		channels = append(channels, models.ChannelPush)
	}
	if user.Notifications.Email {
		channels = append(channels, models.ChannelEmail)
	}
	if user.Notifications.SMS {
		channels = append(channels, models.ChannelSMS)
	}
	if len(channels) == 0 {
		slog.Warn("notification.preferences.none_set", "user_id", user.Notifications.UserID)
		channels = []models.NotificationChannel{models.ChannelEmail}
	}

	user.Notifications.PreferredChannels = channels

	return user
}
