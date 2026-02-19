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
)

type NotificationPayload struct {
	UserID        int
	Type          NotificationType
	Title         string
	Body          string
	Data          map[string]string
	Email         string
	PhoneNumber   string
	ExpoPushToken string
	Preferences   []NotificationChannel
}
