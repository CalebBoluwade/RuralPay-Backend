package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config struct for package initialization
type Config struct{}

func init() {
	// Initialize config
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	// Read config file
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("Warning: Could not read config file: %v\n", err)
	}

	// Direct environment variable fallback
	_ = viper.BindEnv("database.host", "DATABASE_HOST")
	_ = viper.BindEnv("database.port", "DATABASE_PORT")
	_ = viper.BindEnv("database.user", "DATABASE_USER")
	_ = viper.BindEnv("database.password", "DATABASE_PASSWORD")
	_ = viper.BindEnv("database.name", "DATABASE_NAME")
	_ = viper.BindEnv("database.ssl_mode", "DATABASE_SSL_MODE")

	_ = viper.BindEnv("redis.host", "REDIS_HOST")
	_ = viper.BindEnv("redis.port", "REDIS_PORT")
	_ = viper.BindEnv("redis.password", "REDIS_PASSWORD")
	_ = viper.BindEnv("redis.db", "REDIS_DB")

	_ = viper.BindEnv("hsm.master_key", "HSM_MASTER_KEY")
	_ = viper.BindEnv("hsm.salt", "HSM_SALT")
	_ = viper.BindEnv("hsm.key_store_path", "HSM_KEY_STORE_PATH")
	_ = viper.BindEnv("hsm.type", "HSM_TYPE")
	viper.SetDefault("hsm.type", "software")

	_ = viper.BindEnv("jwt.secret_key", "JWT_SECRET_KEY")
	_ = viper.BindEnv("jwt.expiry_minutes", "JWT_EXPIRY_MINUTES")
	viper.SetDefault("jwt.expiry_minutes", 15)

	_ = viper.BindEnv("auth.use_encrypted_password", "AUTH_USE_ENCRYPTED_PASSWORD")
	viper.SetDefault("auth.use_encrypted_password", true)

	_ = viper.BindEnv("jwt.issuer", "JWT_ISSUER")
	_ = viper.BindEnv("jwt.audience", "JWT_AUDIENCE")
	_ = viper.BindEnv("argon2.time", "ARGON2_TIME")
	_ = viper.BindEnv("argon2.memory", "ARGON2_MEMORY")
	_ = viper.BindEnv("argon2.threads", "ARGON2_THREADS")
	_ = viper.BindEnv("argon2.key_length", "ARGON2_KEY_LENGTH")
	_ = viper.BindEnv("argon2.salt_length", "ARGON2_SALT_LENGTH")

	_ = viper.BindEnv("smtp.name", "SMTP_NAME")
	_ = viper.BindEnv("smtp.host", "SMTP_HOST")
	_ = viper.BindEnv("smtp.port", "SMTP_PORT")
	_ = viper.BindEnv("smtp.user", "SMTP_USER")
	_ = viper.BindEnv("smtp.password", "SMTP_PASSWORD")
	_ = viper.BindEnv("smtp.from", "SMTP_FROM")
	_ = viper.BindEnv("smtp.ssl", "SMTP_SSL")
	viper.SetDefault("smtp.ssl", false)

	_ = viper.BindEnv("sms.url", "SMS_SERVICE_URL")
	_ = viper.BindEnv("templates.dir", "TEMPLATES_DIR")
	viper.SetDefault("smtp.port", 587)
	viper.SetDefault("templates.dir", "./static/templates/email")

	_ = viper.BindEnv("nibss.base_url", "NIBSS_BASE_URL")
	_ = viper.BindEnv("nibss.api_key", "NIBSS_API_KEY")
	_ = viper.BindEnv("nibss.bvn_url", "NIBSS_BVN_URL")

	_ = viper.BindEnv("UseNIBSSISOzNIPSwitch", "UseNIBSSISOzNIPSwitch")

	// NIBSS NIP (SOAP) configuration
	_ = viper.BindEnv("nip.bank_code", "NIP_BANK_CODE")
	_ = viper.BindEnv("nip.payment_prefix", "NIP_PAYMENT_PREFIX")
	_ = viper.BindEnv("nip.crypto_url", "NIP_CRYPTO_URL")
	_ = viper.BindEnv("nip.core_url", "NIP_CORE_URL")
	_ = viper.BindEnv("nip.encryption_base_url", "NIP_ENCRYPTION_BASE_URL")
	_ = viper.BindEnv("nip.base_url", "NIP_BASE_URL")
	_ = viper.BindEnv("nip.tsq_base_url", "NIP_TSQ_BASE_URL")
	_ = viper.BindEnv("nip.timeout_seconds", "NIP_TIMEOUT_SECONDS")
	viper.SetDefault("nip.timeout_seconds", 60)
	_ = viper.BindEnv("nip.response_codes_path", "NIP_RESPONSE_CODES_PATH")
	viper.SetDefault("nip.response_codes_path", "./internal/config/nip_response_codes.json")

	// ISO 20022 signing keys
	_ = viper.BindEnv("iso20022.signing_key_path", "ISO20022_SIGNING_KEY_PATH")
	_ = viper.BindEnv("iso20022.nibss_pub_key_path", "ISO20022_NIBSS_PUB_KEY_PATH")

	// ISO 20022 callback authentication
	_ = viper.BindEnv("iso20022.callback.hmac_secret", "ISO20022_CALLBACK_HMAC_SECRET")
	_ = viper.BindEnv("iso20022.callback.tls.enabled", "ISO20022_CALLBACK_TLS_ENABLED")
	viper.SetDefault("iso20022.callback.tls.enabled", false)
	_ = viper.BindEnv("iso20022.callback.tls.allowed_issuers", "ISO20022_CALLBACK_TLS_ALLOWED_ISSUERS")
	_ = viper.BindEnv("iso20022.callback.tls.whitelisted_serials", "ISO20022_CALLBACK_TLS_WHITELISTED_SERIALS")
	_ = viper.BindEnv("iso20022.callback.require_auth", "ISO20022_CALLBACK_REQUIRE_AUTH")
	viper.SetDefault("iso20022.callback.require_auth", true)

	// NIBSS ISO 20022 per-message-family endpoints
	_ = viper.BindEnv("nibss.iso20022.base.url", "NIBSS_ISO20022_BASE_URL")

	// ISO 8583 (card payment settlement)
	_ = viper.BindEnv("nibss.iso8583.base_url", "NIBSS_ISO8583_BASE_URL")
	// _ = viper.BindEnv("nibss.iso8583.ssl_cert_path", "NIBSS_ISO8583_SSL_CERT_PATH")
	// _ = viper.BindEnv("nibss.iso8583.ssl_key_path", "NIBSS_ISO8583_SSL_KEY_PATH")

	_ = viper.BindEnv("nibss.iso8583.component_key_1", "NIBSS_ISO8583_ComponentKey1")
	_ = viper.BindEnv("nibss.iso8583.component_key_2", "NIBSS_ISO8583_ComponentKey2")

	_ = viper.BindEnv("iso8583.acquiring_institution_id", "ISO8583_ACQUIRING_INSTITUTION_ID")
	_ = viper.BindEnv("iso8583.forwarding_institution_id", "ISO8583_FORWARDING_INSTITUTION_ID")
	_ = viper.BindEnv("iso8583.receiving_institution_id", "ISO8583_RECEIVING_INSTITUTION_ID")
	_ = viper.BindEnv("iso8583.terminal_id", "ISO8583_TERMINAL_ID")
	_ = viper.BindEnv("iso8583.card_acceptor_id", "ISO8583_CARD_ACCEPTOR_ID")
	_ = viper.BindEnv("iso8583.card_acceptor_name", "ISO8583_CARD_ACCEPTOR_NAME")
	_ = viper.BindEnv("iso8583.merchant_category_code", "ISO8583_MERCHANT_CATEGORY_CODE")
	viper.SetDefault("iso8583.merchant_category_code", "5011")

	_ = viper.BindEnv("log.file", "LOG_FILE")
	viper.SetDefault("log.file", "./logs/app.log")
	_ = viper.BindEnv("log.max_size_mb", "LOG_MAX_SIZE_MB")
	viper.SetDefault("log.max_size_mb", 1)
	_ = viper.BindEnv("log.max_backups", "LOG_MAX_BACKUPS")
	viper.SetDefault("log.max_backups", 1)
	_ = viper.BindEnv("log.max_age_days", "LOG_MAX_AGE_DAYS")
	viper.SetDefault("log.max_age_days", 1)

	_ = viper.BindEnv("app.env", "APP_ENV")
	viper.SetDefault("app.env", "production")
	_ = viper.BindEnv("app.version", "APP_VERSION")
	viper.SetDefault("app.version", "v1.0.0")

	_ = viper.BindEnv("app.base_url", "APP_BASE_URL")
	viper.SetDefault("app.base_url", "http://localhost:8080")

	_ = viper.BindEnv("cors.allowed_origins", "CORS_ALLOWED_ORIGINS")
	viper.SetDefault("cors.allowed_origins", "http://localhost:3000")

	_ = viper.BindEnv("app.name", "APP_NAME")
	viper.SetDefault("app.name", "RuralPay")

	_ = viper.BindEnv("app.scheme", "APP_SCHEME")
	viper.SetDefault("app.scheme", "ruralpay")

	_ = viper.BindEnv("app.qr_route", "APP_QR_ROUTE")

	_ = viper.BindEnv("session.inactivity_ttl_minutes", "SESSION_INACTIVITY_TTL_MINUTES")
	viper.SetDefault("session.inactivity_ttl_minutes", 5)

	_ = viper.BindEnv("session.absolute_ttl_minutes", "SESSION_ABSOLUTE_TTL_MINUTES")
	viper.SetDefault("session.absolute_ttl_minutes", 15)

	_ = viper.BindEnv("user.default_daily_limit", "USER_DEFAULT_DAILY_LIMIT")
	_ = viper.BindEnv("user.default_single_tx_limit", "USER_DEFAULT_SINGLE_TX_LIMIT")

	viper.SetDefault("user.default_daily_limit", 500000)
	viper.SetDefault("user.default_single_tx_limit", 100000)
}
