package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	go_expo "github.com/montovaneli/go-expo-notification"
	"github.com/ruralpay/backend/internal/models"
)

type NotificationService struct {
	expoURL    string
	emailURL   string
	smsURL     string
	httpClient *http.Client
}

func NewNotificationService() *NotificationService {
	return &NotificationService{
		expoURL:  "https://exp.host/--/api/v2/push/send",
		emailURL: os.Getenv("EMAIL_SERVICE_URL"),
		smsURL:   os.Getenv("SMS_SERVICE_URL"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (ns *NotificationService) SendPaymentNotification(transaction *models.Transaction, user *models.User, notifType models.NotificationType) error {
	payload := ns.buildPaymentPayload(transaction, user, notifType)
	return ns.Route(payload)
}

func (ns *NotificationService) Route(payload *models.NotificationPayload) error {
	// Push notifications are always sent
	if payload.ExpoPushToken != "" {
		go ns.sendPush(payload)
	}

	// Route to other channels based on preferences
	for _, channel := range payload.Preferences {
		switch channel {
		case models.ChannelEmail:
			if payload.Email != "" {
				go ns.sendEmail(payload)
			}
		case models.ChannelSMS:
			if payload.PhoneNumber != "" {
				go ns.sendSMS(payload)
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

	// 4. Send the notification
	_, err := p.Publish(m)
	if err != nil {
		log.Printf("Error Sending Notification: %v", err)
		return err
	}

	fmt.Println("Notification Sent Successfully!")

	return nil
}

func (ns *NotificationService) sendEmail(payload *models.NotificationPayload) error {
	if ns.emailURL == "" {
		return nil
	}

	emailPayload := map[string]interface{}{
		"to":      payload.Email,
		"subject": payload.Title,
		"body":    payload.Body,
		"data":    payload.Data,
	}

	body, _ := json.Marshal(emailPayload)
	req, err := http.NewRequest("POST", ns.emailURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := ns.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

func (ns *NotificationService) sendSMS(payload *models.NotificationPayload) error {
	if ns.smsURL == "" {
		return nil
	}

	smsPayload := map[string]interface{}{
		"to":      payload.PhoneNumber,
		"message": fmt.Sprintf("%s\n%s", payload.Title, payload.Body),
	}

	body, _ := json.Marshal(smsPayload)
	req, err := http.NewRequest("POST", ns.smsURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := ns.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

func (ns *NotificationService) buildPaymentPayload(transaction *models.Transaction, user *models.User, notifType models.NotificationType) *models.NotificationPayload {
	var title, body string

	switch notifType {
	case models.PaymentReceived:
		title = "Payment Received"
		body = fmt.Sprintf("You received %.2f %s", float64(transaction.Amount)/100, transaction.Currency)
	case models.PaymentSent:
		title = "Payment Sent"
		body = fmt.Sprintf("You sent %.2f %s", float64(transaction.Amount)/100, transaction.Currency)
	case models.PaymentFailed:
		title = "Payment Failed"
		body = fmt.Sprintf("Payment of %.2f %s failed", float64(transaction.Amount)/100, transaction.Currency)
	case models.PaymentPending:
		title = "Payment Pending"
		body = fmt.Sprintf("Payment of %.2f %s is pending", float64(transaction.Amount)/100, transaction.Currency)
	}

	return &models.NotificationPayload{
		UserID:        user.ID,
		Type:          notifType,
		Title:         title,
		Body:          body,
		Email:         user.Email,
		PhoneNumber:   user.PhoneNumber,
		ExpoPushToken: user.DeviceID,
		Data: map[string]string{
			"transactionId": transaction.TransactionID,
			"amount":        fmt.Sprintf("%.2f", float64(transaction.Amount)/100),
			"currency":      transaction.Currency,
			"status":        transaction.Status,
		},
		Preferences: getUserPreferences(user),
	}
}

func getUserPreferences(user *models.User) []models.NotificationChannel {
	// Default preferences - can be extended to read from user settings
	return []models.NotificationChannel{models.ChannelEmail, models.ChannelSMS}
}
