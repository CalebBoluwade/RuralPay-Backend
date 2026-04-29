package models

type NotificationChannel string

const (
	ChannelPush  NotificationChannel = "PUSH"
	ChannelEmail NotificationChannel = "EMAIL"
	ChannelSMS   NotificationChannel = "SMS"
)

type NotificationType string

const (
	PaymentReceived NotificationType = "payment_received"
	PaymentSent     NotificationType = "payment_sent"
	PaymentFailed   NotificationType = "payment_failed"
	PaymentPending  NotificationType = "payment_pending"
	TransactionOTP  NotificationType = "TransactionOTP"
	ForgotPassword  NotificationType = "ForgotPassword"
	ValidateAccount NotificationType = "ValidateAccount"
)

// Notification represents a user notification.
type Notification struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Time    string `json:"time"`
	Read    bool   `json:"read"`
}

type NotificationPayload struct {
	UserID        int
	Type          NotificationType
	Title         string
	Body          string
	Data          map[string]string
	Email         string
	PhoneNumber   string
	ExpoPushToken string
	FeedbackURL   string
	BaseURL       string
}
