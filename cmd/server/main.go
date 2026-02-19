package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/ruralpay/backend/docs"
	"github.com/ruralpay/backend/internal/database"
	"github.com/ruralpay/backend/internal/handlers"
	"github.com/ruralpay/backend/internal/hsm"
	mW "github.com/ruralpay/backend/internal/middleware"
	"github.com/ruralpay/backend/internal/services"
	"github.com/spf13/viper"
	httpSwagger "github.com/swaggo/http-swagger"
)

// @title NFC Payments Backend API
// @version 1.0
// @description API for NFC-based payment processing system
// @host localhost:8080
// @BasePath /api/v1
// @schemes http https

// Global HSM instance
var hsmInstance hsm.HSMInterface

func main() {
	// Initialize config
	viper.SetConfigFile(".env") // explicitly point to .env file
	viper.AutomaticEnv()        // allow environment variables to override .env
	viper.ReadInConfig()        // read .env file

	// Set environment variable prefix
	viper.SetEnvPrefix("")

	viper.BindEnv("database.host", "DATABASE_HOST")
	viper.BindEnv("database.port", "DATABASE_PORT")
	viper.BindEnv("database.user", "DATABASE_USER")
	viper.BindEnv("database.password", "DATABASE_PASSWORD")
	viper.BindEnv("database.name", "DATABASE_NAME")
	viper.BindEnv("database.ssl_mode", "DATABASE_SSL_MODE")

	viper.BindEnv("redis.host", "REDIS_HOST")
	viper.BindEnv("redis.port", "REDIS_PORT")
	viper.BindEnv("redis.password", "REDIS_PASSWORD")
	viper.BindEnv("redis.db", "REDIS_DB")

	viper.BindEnv("hsm.master_key", "HSM_MASTER_KEY")
	viper.BindEnv("hsm.salt", "HSM_SALT")
	viper.BindEnv("hsm.key_store_path", "HSM_KEY_STORE_PATH")
	viper.BindEnv("jwt.secret_key", "JWT_SECRET_KEY")
	viper.BindEnv("jwt.expiry_hours", "JWT_EXPIRY_HOURS")
	viper.BindEnv("jwt.issuer", "JWT_ISSUER")
	viper.BindEnv("jwt.audience", "JWT_AUDIENCE")
	viper.BindEnv("argon2.time", "ARGON2_TIME")
	viper.BindEnv("argon2.memory", "ARGON2_MEMORY")
	viper.BindEnv("argon2.threads", "ARGON2_THREADS")
	viper.BindEnv("argon2.key_length", "ARGON2_KEY_LENGTH")
	viper.BindEnv("argon2.salt_length", "ARGON2_SALT_LENGTH")

	viper.BindEnv("nibss.base_url", "NIBSS_BASE_URL")
	viper.BindEnv("nibss.api_key", "NIBSS_API_KEY")

	viper.BindEnv("PII_ENCRYPTION_KEY", "PII_ENCRYPTION_KEY")

	viper.BindEnv("user.default_daily_limit", "USER_DEFAULT_DAILY_LIMIT")
	viper.BindEnv("user.default_single_tx_limit", "USER_DEFAULT_SINGLE_TX_LIMIT")

	viper.SetDefault("user.default_daily_limit", 500000)
	viper.SetDefault("user.default_single_tx_limit", 100000)

	if err := viper.ReadInConfig(); err != nil {
		log.Printf("Config file not found, using defaults: %v", err)
	}

	// Initialize Swagger docs
	docs.SwaggerInfo.Title = "RuralPay Backend API"
	docs.SwaggerInfo.Description = "Payment Processing API"
	docs.SwaggerInfo.Version = "1.0"
	docs.SwaggerInfo.Host = "localhost:8080"
	docs.SwaggerInfo.BasePath = "/api/v1"
	docs.SwaggerInfo.Schemes = []string{"http", "https"}

	// Initialize services
	db := database.InitDatabase()
	defer db.Close()

	redisClient := database.InitRedis()
	if redisClient != nil {
		defer redisClient.Close()
	}

	hsm, err := hsm.InitHSM(hsm.Config{
		MasterKey:       viper.GetString("hsm.master_key"),
		KeyStorePath:    viper.GetString("hsm.key_store_path"),
		KeyRotationDays: viper.GetInt("hsm.key_rotation_days"),
		Salt:            []byte(viper.GetString("hsm.salt")),
	})
	if err != nil {
		log.Fatalf("Failed to Initialize HSM: %v", err)
	}

	// Sync HSM keys to database
	hsmKeyService := services.NewHSMKeyService(db, hsm)
	if err := hsmKeyService.SyncKeysToDatabase(); err != nil {
		log.Printf("Warning: Failed to Sync HSM Keys to Database: %v", err)
	} else {
		log.Println("HSM Keys Synced to Database Successfully")
	}
	defer func() {
		if logger, ok := hsmInstance.(interface{ Close() error }); ok {
			if err := logger.Close(); err != nil {
				log.Printf("Failed to close HSM logger: %v", err)
			}
		}
	}()

	// Initialize services
	authService := services.NewAuthService(db, redisClient)
	bankService := services.NewBankService()
	accountService := services.NewAccountService(db, redisClient)
	cardService := services.NewCardService(db, hsm)
	iso20022Service := services.NewISO20022Service()
	merchantService := services.NewMerchantService(db)
	transactionQueryService := services.NewTransactionQueryService(db)
	qrService := services.NewQRService(db, redisClient)

	// Initialize unified payment handler
	paymentHandler := handlers.NewPaymentHandler(db, redisClient, hsm)
	qrHandler := handlers.NewQRHandler(qrService)

	// Initialize auth middleware with Redis
	mW.InitAuthMiddleware(redisClient, authService)

	// Setup router
	r := chi.NewRouter()

	// Middleware
	r.Use(mW.SecurityHeaders)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(middleware.Timeout(60 * time.Second))

	// CORS
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*.ruralpayments.com"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	// Swagger documentation
	r.Get("/swagger/*", httpSwagger.Handler(
		httpSwagger.URL("http://localhost:8080/swagger/doc.json"),
	))

	// Serve OpenAPI spec
	r.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./api/openapi.yaml")
	})

	// Static file server for bank logos
	r.Handle("/static/bank-logos/*", http.StripPrefix("/static/bank-logos/",
		mW.StaticFileServer("./static/bank-logos")))

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Public endpoints (no auth required)
		r.Post("/auth/register", authService.Register)
		r.Post("/auth/login", authService.Login)
		r.Post("/auth/logout", authService.Logout)
		r.Post("/auth/forgot-password", authService.ForgotPassword)
		r.Post("/auth/reset-password", authService.ResetPassword)
		r.Get("/banks", bankService.GetAllBanks)
		r.Post("/accounts/verify-otp", authService.VerifyOTP)

		// Protected endpoints (auth required)
		r.Group(func(r chi.Router) {
			r.Use(mW.AuthMiddleware)

			r.Get("/auth/account", authService.GetUserAccount)

			// Unified payment endpoint
			r.Post("/payments", paymentHandler.HandlePayment)

			// Transaction query endpoints
			r.Get("/transactions", transactionQueryService.ListTransactions)
			r.Get("/transactions/{txId}", transactionQueryService.GetTransaction)
			r.Get("/transactions/recent", transactionQueryService.GetRecentTransactions)

			// Account endpoints
			r.Post("/accounts/link", accountService.LinkAccount)
			r.Get("/accounts/name-enquiry", accountService.AccountNameEnquiry)
			r.Get("/accounts/balance-enquiry", accountService.AccountBalanceEnquiry)
			r.Put("/accounts/limits", accountService.UpdateUserLimits)
			r.Post("/accounts/validate-bvn", accountService.ValidateBVN)
			r.Get("/accounts/virtual-account", accountService.GetVirtualAccount)

			// Card provisioning endpoints
			r.Get("/cards/bins", cardService.QueryCardBin)
			r.Post("/cards/provision", cardService.ProvisionCard)
			r.Post("/cards/activate", cardService.ActivateCard)
			r.Get("/cards/{cardId}", cardService.GetCard)
			r.Put("/cards/{cardId}/suspend", cardService.SuspendCard)

			// ISO 20022 endpoints
			r.Post("/iso20022/convert", iso20022Service.ConvertToISO20022)
			r.Post("/iso20022/settlement", iso20022Service.ProcessSettlement)

			merchantRoute := "/merchants"

			// Merchant endpoints
			r.Post(merchantRoute, merchantService.OnboardMerchant)
			r.Put(merchantRoute, merchantService.UpdateMerchant)
			r.Get(merchantRoute, merchantService.GetMerchantData)
			r.Get("/merchants/list", merchantService.ListMerchants)
			r.Put("/merchants/status", merchantService.UpdateMerchantStatus)

			// QR endpoints
			r.Post("/qr/generate", qrHandler.GenerateQR)
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Start server
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		log.Printf("Server starting on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown failed: %v", err)
	}
	log.Println("Server stopped gracefully")
}
