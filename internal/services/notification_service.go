package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	mail "github.com/go-mail/mail/v2"
	go_expo "github.com/montovaneli/go-expo-notification"
	"github.com/ruralpay/backend/internal/models"
	"github.com/spf13/viper"
)

type NotificationService struct {
	expoURL      string
	smtpHost     string
	smtpPort     int
	smtpUser     string
	smtpPassword string
	smtpFrom     string
	smtpName     string
	templatesDir string
	smsURL       string
	httpClient   *http.Client
}

func NewNotificationService() *NotificationService {
	return &NotificationService{
		expoURL:      "https://exp.host/--/api/v2/push/send",
		smtpHost:     viper.GetString("smtp.host"),
		smtpPort:     viper.GetInt("smtp.port"),
		smtpUser:     viper.GetString("smtp.user"),
		smtpPassword: viper.GetString("smtp.password"),
		smtpFrom:     viper.GetString("smtp.from"),
		smtpName:     viper.GetString("smtp.name"),
		templatesDir: viper.GetString("templates.dir"),
		smsURL:       viper.GetString("sms.url"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (ns *NotificationService) SendPaymentNotification(transaction *models.TransactionRecord, user *models.User, notifType models.NotificationType) error {
	payload := ns.buildPaymentPayload(transaction, user, notifType)
	return ns.Route(payload)
}

func (ns *NotificationService) Route(payload *models.NotificationPayload) error {
	slog.Info("notification.routing", "user_id", payload.UserID, "title", payload.Title)

	if payload.ExpoPushToken != "" {
		slog.Info("notification.dispatch", "channel", "push", "user_id", payload.UserID)
		go ns.sendPush(payload)
	}

	for _, channel := range payload.Preferences {
		switch channel {
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
	p := go_expo.NewPushClient(nil)

	// 2. Define the recipient
	m := &go_expo.PushMessage{
		To:       []go_expo.ExponentPushToken{go_expo.ExponentPushToken(payload.ExpoPushToken)},
		Title:    payload.Title,
		Body:     payload.Body,
		Data:     payload.Data,
		Sound:    "default",
		Priority: "default",
	}

	_, err := p.Publish(m)
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
	if err := d.DialAndSend(m); err != nil {
		slog.Error("notification.otp.failed", "error", err)
		return err
	}

	slog.Info("notification.otp.sent", "type", notifType, "email", email)
	return nil
}

func (ns *NotificationService) SendOTPSmS(phoneNumber, otp, expiresIn string, notifType models.NotificationType) error {
	if ns.smsURL == "" {
		slog.Warn("sms.notification.otp.skipped", "reason", "smtp_not_configured")
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

func (ns *NotificationService) buildPaymentPayload(transaction *models.TransactionRecord, user *models.User, notifType models.NotificationType) *models.NotificationPayload {
	var title, body string

	switch notifType {
	case models.PaymentReceived:
		title = "Payment Received"
		body = fmt.Sprintf("You Received %s %.2f From %s", transaction.Currency, float64(transaction.Amount), transaction.FromAccountID)
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

	return &models.NotificationPayload{
		Type:          notifType,
		Title:         title,
		Body:          body,
		Email:         user.Email,
		PhoneNumber:   user.PhoneNumber,
		ExpoPushToken: user.ExpoPushToken,
		FeedbackURL:   fmt.Sprintf("%s/api/v1/feedback?transaction_id=%s", viper.GetString("app.base_url"), transaction.TransactionID),
		BaseURL:       viper.GetString("app.base_url"),
		Data: map[string]string{
			"transactionId":       transaction.TransactionID,
			"amount":              fmt.Sprintf("%.2f", float64(transaction.Amount)),
			"currency":            transaction.Currency,
			"status":              transaction.Status,
			"statusClass":         statusClass[notifType],
			"senderFirstName":     user.FirstName,
			"beneficiaryName":     fmt.Sprintf("%v", transaction.Metadata["beneficiaryName"]),
			"beneficiaryBankName": fmt.Sprintf("%v", transaction.Metadata["beneficiaryBankName"]),
			"beneficiaryAcct":     transaction.ToAccountID,
			"fee":                 fmt.Sprintf("%.2f", float64(transaction.Fee)),
			"reference":           fmt.Sprintf("%v", transaction.Metadata["reference"]),
			"date":                transaction.CreatedAt.Format("02 Jan 2006, 03:04 PM"),
		},
		Preferences: getUserPreferences(user),
	}
}

func getUserPreferences(user *models.User) []models.NotificationChannel {
	// Default preferences - can be extended to read from user settings
	return []models.NotificationChannel{models.ChannelEmail, models.ChannelSMS}
}
