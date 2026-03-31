package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/ruralpay/backend/internal/utils"

	"github.com/ruralpay/backend/docs"
	"github.com/ruralpay/backend/internal/config"
	"github.com/ruralpay/backend/internal/database"
	"github.com/ruralpay/backend/internal/handlers"
	"github.com/ruralpay/backend/internal/hsm"
	appLogger "github.com/ruralpay/backend/internal/logger"
	mW "github.com/ruralpay/backend/internal/middleware"
	"github.com/ruralpay/backend/internal/models"
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
	// Config is initialized via init() function in config package
	_ = config.Config{} // Import config package to trigger init()

	// Initialize PII-masking structured logger
	logPath := viper.GetString("log.file")
	structuredLogger, logCloser, err := appLogger.New(logPath, &slog.HandlerOptions{Level: slog.LevelInfo}, appLogger.RotationConfig{
		MaxSizeMB:  viper.GetInt("log.max_size_mb"),
		MaxBackups: viper.GetInt("log.max_backups"),
		MaxAgeDays: viper.GetInt("log.max_age_days"),
		Compress:   true,
	})
	if err != nil {
		slog.Error("Failed to Initialize logger", "error", err)
		os.Exit(1)
	}
	defer logCloser.Close()

	structuredLogger = structuredLogger.With(
		slog.String("app", "RuralPay"),
		slog.String("env", viper.GetString("app.env")),
		slog.String("version", viper.GetString("app.version")),
	)
	slog.SetDefault(structuredLogger)

	// Initialize Swagger docs
	docs.SwaggerInfo.Title = "RuralPay Backend API"
	docs.SwaggerInfo.Description = "RuralPay Payment Processing API"
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

	// Validate HSM configuration
	masterKey := viper.GetString("hsm.master_key")
	if masterKey == "" {
		slog.Error("server.hsm.config_missing", "error", "HSM_MASTER_KEY environment variable is required")
		os.Exit(1)
	}
	slog.Info("server.hsm.config_debug", "master_key_length", len(masterKey), "has_value", masterKey != "")

	hsm, err := hsm.InitHSM(hsm.Config{
		HSMType:         viper.GetString("hsm.type"),
		MasterKey:       masterKey,
		KeyStorePath:    viper.GetString("hsm.key_store_path"),
		KeyRotationDays: viper.GetInt("hsm.key_rotation_days"),
		Salt:            []byte(viper.GetString("hsm.salt")),
	})
	if err != nil {
		slog.Error("server.hsm.init_failed", "error", err)
		os.Exit(1)
	}

	// Sync HSM keys to database
	hsmKeyService := services.NewHSMKeyService(db, hsm)
	if err := hsmKeyService.SyncKeysToDatabase(); err != nil {
		slog.Warn("server.hsm.sync_failed", "error", err)
	} else {
		slog.Info("server.hsm.sync_success")
	}
	defer func() {
		if logger, ok := hsmInstance.(interface{ Close() error }); ok {
			if err := logger.Close(); err != nil {
				slog.Error("server.hsm.close_failed", "error", err)
			}
		}
	}()

	// Initialize services
	notificationService := services.NewNotificationService(db)
	userService := services.NewUserService(db, redisClient, notificationService)
	bankService := services.NewBankService()
	accountService := services.NewAccountService(db, redisClient)
	cardService := services.NewCardService(db, hsm)
	iso20022Service := services.NewISO20022Service()
	merchantService := services.NewMerchantService(db)
	transactionQueryService := services.NewTransactionQueryService(db)
	voucherService := services.NewVoucherService(db)

	// Initialize unified payment handler
	paymentHandler := handlers.NewPaymentHandler(db, redisClient, hsm)
	feedbackHandler := handlers.NewFeedbackHandler(db, notificationService)
	isoCallbackHandler := handlers.NewISO20022CallbackHandler(iso20022Service)

	// Initialize auth middleware with Redis
	mW.InitAuthMiddleware(redisClient, userService, models.SessionConfig{
		InactivityTTL: time.Duration(viper.GetInt("session.inactivity_ttl_minutes")) * time.Minute,
		AbsoluteTTL:   time.Duration(viper.GetInt("session.absolute_ttl_minutes")) * time.Minute,
	})

	// Setup router
	r := chi.NewRouter()

	// Middleware
	r.Use(mW.SecurityHeaders)
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(mW.StructuredLogger(structuredLogger))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Heartbeat("/health"))
	r.Use(middleware.Compress(5))
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(mW.RateLimiter(redisClient, mW.GlobalLimit))

	//CORS
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
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

	// Serve OpenAPI spec (hardcoded path — not derived from request input)
	openAPIPath, err := filepath.Abs("./api/openapi.yaml")
	if err != nil {
		slog.Error("server.openapi.path_resolve_failed", "error", err)
		os.Exit(1)
	}
	r.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, openAPIPath)
	})

	// Static file server for bank logos
	r.Handle("/static/bank-logos/*", http.StripPrefix("/static/bank-logos/",
		mW.StaticFileServer("./static/bank-logos")))

	// ISO20022 callback endpoints — top-level, no auth, inbound pushes from NIBSS
	r.Post("/pacs008", isoCallbackHandler.ReceivePacs008)
	r.Post("/pacs002", isoCallbackHandler.ReceivePacs002)
	r.Post("/pacs028", isoCallbackHandler.ReceivePacs028)
	r.Post("/acmt023", isoCallbackHandler.ReceiveAcmt023)
	r.Post("/acmt024", isoCallbackHandler.ReceiveAcmt024)

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "No Route Found", http.StatusBadRequest, nil)
		})

		// Public endpoints (no auth required)

		// Strict rate limit: 5 req / 15 min — login, password reset
		r.Group(func(r chi.Router) {
			r.Use(mW.RateLimiter(redisClient, mW.AuthStrict))
			r.Post("/auth/login", userService.UserLogin)
			r.Post("/auth/forgot-password", userService.ForgotPassword)
			r.Post("/auth/reset-password", userService.ResetPassword)
		})

		// General rate limit: 10 req / 10 min — register, refresh, OTP
		r.Group(func(r chi.Router) {
			r.Use(mW.RateLimiter(redisClient, mW.AuthGeneral))
			r.Post("/auth", userService.RegisterNewUser)
			r.Put("/auth", userService.EditUserProfile)
			r.Post("/auth/refresh", userService.RefreshToken)
			r.Post("/account/send-bvn-otp", accountService.GenerateBVNOTP)
			r.Post("/account/validate-bvn-otp", accountService.ValidateBVNOTP)
		})

		r.Get("/banks", bankService.GetAllBanks)

		r.Get("/encryption/keys", hsmKeyService.GetUserPublicKeys)
		r.Put("/encryption/keys", hsmKeyService.CreateNewKeysExternal)

		// Feedback endpoints (public — clicked from email links)
		r.Get("/feedback", feedbackHandler.HandleTransactionRating)
		r.Get("/feedback/referral", feedbackHandler.HandleReferralSource)
		r.Get("/feedback/deletion-reason", feedbackHandler.HandleDeletionReason)
		r.Get("/feedback/confirm-login", feedbackHandler.HandleConfirmLogin)

		// Protected endpoints (auth required)
		r.Group(func(r chi.Router) {
			r.Use(mW.AuthSessionMiddleware)

			r.Delete("/auth", userService.DeleteUserProfile)
			r.Get("/auth/account", userService.GetUserAccount)
			r.Post("/auth/logout", userService.LogoutUser)
			r.Post("/account/send-otp", accountService.GenerateUserOTP)

			// Account endpoints
			r.Post("/account/link", accountService.LinkAccount)
			r.Post("/account/unlink/{accountNumber}", accountService.UnlinkAccount)
			r.Get("/account/name-enquiry", accountService.AccountNameEnquiry)
			r.Get("/account/balance-enquiry", accountService.AccountBalanceEnquiry)
			r.Put("/account/limits", accountService.UpdateUserLimits)
			r.Get("/account/virtual-account", accountService.GetVirtualAccount)
			r.Get("/account/beneficiaries", accountService.GetBeneficiaries)
			r.Get("/account/notifications", notificationService.GetUserNotifications)

			// QR endpoints
			r.Post("/account/qr", accountService.GenerateQR)
			r.Get("/account/qr", accountService.ProcessQR)

			r.Post("/account/ussd", accountService.GenerateUSSDCode)
			r.Get("/account/ussd", accountService.ValidateUSSDCode)

			// Unified payment endpoint
			r.Post("/payments", paymentHandler.HandlePayment)

			// Transaction Query endpoints
			r.Get("/transaction", transactionQueryService.GetRecentTransactions)
			r.Get("/transaction/{txId}", transactionQueryService.GetTransaction)

			// Card Endpoints
			r.Get("/card/bin", cardService.QueryCardBin)
			r.Post("/card/provision", cardService.ProvisionCard)
			r.Post("/card/activate", cardService.ActivateCard)
			r.Get("/card/{cardId}", cardService.GetCard)
			r.Put("/card/{cardId}/suspend", cardService.SuspendCard)

			// Voucher endpoints
			r.Get("/vouchers", voucherService.FetchVouchers)

			// ISO 20022 endpoints
			r.Post("/iso20022/convert", iso20022Service.ConvertToISO20022)
			r.Post("/iso20022/settlement", iso20022Service.ProcessSettlement)
		})

		r.Group(func(r chi.Router) {
			r.Use(mW.AuthSessionMiddleware)

			merchantRoute := "/merchant"

			// Merchant endpoints
			r.Get(merchantRoute, merchantService.GetMerchantData)
			r.Post(merchantRoute, merchantService.OnboardMerchant)

			r.Patch("/merchant/account/{accountNumber}", merchantService.UpdateMerchantBusinessAccount)
			r.Put(merchantRoute, merchantService.UpdateMerchant)
			r.Get("/merchant/list", merchantService.ListMerchants)
			r.Put("/merchant/status", merchantService.UpdateMerchantStatus)
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
		slog.Info("server.starting", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server.failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	slog.Info("server.shutting_down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("server.shutdown_failed", "error", err)
		os.Exit(1)
	}
	slog.Info("server.stopped")
}
