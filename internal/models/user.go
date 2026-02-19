package models

import "time"

type DeviceInfo struct {
	Platform         string `json:"os" validate:"required,oneof=iOS android web" example:"iOS"` // Device OS platform
	Model            string `json:"model" validate:"required" example:"iPhone 12"`              // Device model
	OSVersion        string `json:"osVersion" validate:"required" example:"iOS 14.0"`           // Device OS version
	IsPhysicalDevice bool   `json:"isPhysicalDevice" example:"true"`                            // Whether the device is physical or an emulator
}

// LoginRequest represents the login request payload
// @Description Login request structure
type LoginRequest struct {
	Identifier string     `json:"identifier" validate:"required" example:"+2348012345678"`  // User phone number, email, or username
	Password   string     `json:"password" validate:"required,min=6" example:"password123"` // User password
	DeviceInfo DeviceInfo `json:"deviceInfo" validate:"required"`                           // Device information for login
}

// RegisterRequest represents the registration request payload
// @Description Registration request structure
type RegisterRequest struct {
	Email         string `json:"Email" validate:"required,email" example:"user@example.com"`                        // User email address
	Username      string `json:"Username" validate:"required,min=3" example:"johndoe"`                              // Username
	Password      string `json:"Password" validate:"required,min=6" example:"password123"`                          // User password
	FirstName     string `json:"FirstName" validate:"required,min=2" example:"John"`                                // User first name
	LastName      string `json:"LastName" validate:"required,min=2" example:"Doe"`                                  // User last name
	BVN           string `json:"BVN" validate:"required,len=11" example:"12345678901"`                              // Bank Verification Number
	PhoneNumber   string `json:"PhoneNumber" validate:"required" example:"+2348012345678"`                          // Phone number
	ExpoPushToken string `json:"pushToken" validate:"required" example:"ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]"` // Expo push token for notifications
}

// AuthResponse represents the authentication response
// @Description Authentication response structure
type AuthResponse struct {
	Token string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."` // JWT token
	User  User   `json:"user"`                                                    // User information
}

// User represents the user model
// @Description User model structure

// User represents user information
// @Description User structure
type User struct {
	ID                  int       `json:"id" example:"1"`                       // User ID
	Email               string    `json:"email" example:"user@example.com"`     // User email
	FirstName           string    `json:"FirstName" example:"John"`             // User first name
	LastName            string    `json:"LastName" example:"Doe"`               // User last name
	AccountId           string    `json:"AccountId" example:"1234567890"`       // User account ID
	Username            string    `json:"Username" example:"johndoe"`           // User username
	PhoneNumber         string    `json:"phoneNumber" example:"+2348012345678"` // User phone number
	BVN                 string    `json:"BVN" example:"12345678901"`            // User BVN
	DeviceID            string    `json:"device_id"`
	Role                string    `json:"role" example:"user"` // User role (e.g., user, merchant)
	Merchant            *Merchant `json:"merchant,omitempty"`  // Merchant information (if user is a merchant)
	LockedUntil         *time.Time
	LastLogin           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	FailedLoginAttempts int `gorm:"default:0"`
}
