package models

import "time"

type Session struct {
	UserID       string `json:"user_id"`
	DeviceID     string `json:"device_id"`
	RefreshHash  string `json:"refresh_hash"`
	LastActivity int64  `json:"last_activity"`
}

type SessionConfig struct {
	InactivityTTL time.Duration
	AbsoluteTTL   time.Duration
}

// DeviceInfo represents device information for login
// @Description Device information structure
type DeviceInfo struct {
	Platform         string `json:"os" validate:"required" example:"iOS"`             // Device OS platform ,oneof=iOS iPadOS Android web
	Model            string `json:"model" validate:"required" example:"iPhone 12"`    // Device model
	OSVersion        string `json:"osVersion" validate:"required" example:"iOS 14.0"` // Device OS version
	IsPhysicalDevice bool   `json:"isPhysicalDevice" example:"true"`                  // Whether the device is physical or an emulator
}

// LoginRequest represents the login request payload
// @Description Login request structure
type LoginRequest struct {
	Identifier    string     `json:"identifier" validate:"required,max=254" example:"+2348012345678"`         // User phone number, email, or username
	Password      string     `json:"password" validate:"required"`                                            // User password
	DeviceInfo    DeviceInfo `json:"deviceInfo" validate:"required"`                                          // Device information for login
	ExpoPushToken string     `json:"pushToken,omitempty" example:"ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]"` // Expo push token for notifications
}

// RegisterRequest represents the registration request payload
// @Description Registration request structure
type RegisterRequest struct {
	Email         string `json:"Email" validate:"required,email,max=254" example:"user@example.com"`     // User email address
	Username      string `json:"Username" validate:"required,min=3,max=30,alphanum" example:"johndoe"`   // Username
	Password      string `json:"Password" validate:"required,min=6,max=72"`                              // User password
	FirstName     string `json:"FirstName" validate:"required,min=2,max=50" example:"John"`              // User first name
	LastName      string `json:"LastName" validate:"required,min=2,max=50" example:"Doe"`                // User last name
	BVN           string `json:"BVN" validate:"required,len=11,numeric" example:"12345678901"`           // Bank Verification Number
	PhoneNumber   string `json:"PhoneNumber" validate:"required,min=10,max=15" example:"+2348012345678"` // Phone number
	IdentityToken string `json:"IdentityToken" validate:"required,alphanum,uppercase,min=6,max=72" example:"1234567890"`
	ExpoPushToken string `json:"pushToken" validate:"" example:"ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]"` // Expo push token for notifications
}

// AuthResponse represents the authentication response
// @Description Authentication response structure
type AuthResponse struct {
	Token        string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`        // JWT token
	RefreshToken string `json:"refreshToken" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."` // JWT refresh token
	User         User   `json:"user"`                                                           // User information
}

type Channel string

const (
	ChannelOTP               Channel = "OTP"
	ChannelBypass            Channel = "BYPASS"
	ChannelFacialRecognition Channel = "FACIAL_RECOGNITION"
)

type UserTransactionLimits struct {
	DailyLimit             float64 `json:"dailyLimit"`
	SingleTransactionLimit float64 `json:"singleTransactionLimit"`
}

type UserNotifications struct {
	UserID     int  `json:"userId"`
	SMS        bool `json:"sms"`
	Email      bool `json:"email"`
	DevicePush bool `json:"devicePush"`
}

// User represents the user model
// @Description User model structure

// User represents user information
// @Description User structure
type User struct {
	ID                  int       `json:"id" example:"1"`                       // User ID
	Email               string    `json:"email" example:"user@example.com"`     // User email
	FirstName           string    `json:"firstName" example:"John"`             // User first name
	LastName            string    `json:"lastName" example:"Doe"`               // User last name
	AccountId           string    `json:"accountId" example:"1234567890"`       // User account ID
	Username            string    `json:"userName" example:"johndoe"`           // User username
	PhoneNumber         string    `json:"phoneNumber" example:"+2348012345678"` // User phone number
	BVN                 string    `json:"BVN" example:"12345678901"`            // User BVN
	IdentityToken       string    `json:"IdentityToken" example:"ABCD1234563FD"`
	ExpoPushToken       string    `json:"pushToken,omitempty" example:"ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]"` // Expo push token for notifications
	Role                string    `json:"role" example:"user"`                                                     // User role (e.g., user, merchant)
	Merchant            *Merchant `json:"merchant,omitempty"`                                                      // Merchant information (if user is a merchant)
	LockedUntil         *time.Time
	LastLogin           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	FailedLoginAttempts int                   `json:"default:0"`
	TransactionLimits   UserTransactionLimits `json:"transactionLimits"`
	Notifications       UserNotifications     `json:"notifications"`
	KYCStatus           string                `json:"kycStatus"`
	KYCLevel            int                   `json:"kycLevel" default:"0"`
}
