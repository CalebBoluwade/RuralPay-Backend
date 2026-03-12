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
	viper.BindEnv("hsm.type", "HSM_TYPE")
	viper.SetDefault("hsm.type", "software")

	viper.BindEnv("jwt.secret_key", "JWT_SECRET_KEY")
	viper.BindEnv("jwt.expiry_minutes", "JWT_EXPIRY_MINUTES")
	viper.BindEnv("jwt.issuer", "JWT_ISSUER")
	viper.BindEnv("jwt.audience", "JWT_AUDIENCE")
	viper.BindEnv("argon2.time", "ARGON2_TIME")
	viper.BindEnv("argon2.memory", "ARGON2_MEMORY")
	viper.BindEnv("argon2.threads", "ARGON2_THREADS")
	viper.BindEnv("argon2.key_length", "ARGON2_KEY_LENGTH")
	viper.BindEnv("argon2.salt_length", "ARGON2_SALT_LENGTH")

	viper.BindEnv("smtp.name", "SMTP_NAME")
	viper.BindEnv("smtp.host", "SMTP_HOST")
	viper.BindEnv("smtp.port", "SMTP_PORT")
	viper.BindEnv("smtp.user", "SMTP_USER")
	viper.BindEnv("smtp.password", "SMTP_PASSWORD")
	viper.BindEnv("smtp.from", "SMTP_FROM")
	viper.BindEnv("sms.url", "SMS_SERVICE_URL")
	viper.BindEnv("templates.dir", "TEMPLATES_DIR")
	viper.SetDefault("smtp.port", 587)
	viper.SetDefault("templates.dir", "./static/templates/email")

	viper.BindEnv("nibss.base_url", "NIBSS_BASE_URL")
	viper.BindEnv("nibss.api_key", "NIBSS_API_KEY")
	viper.BindEnv("nibss.bvn_url", "NIBSS_BVN_URL")

	viper.BindEnv("PII_ENCRYPTION_KEY", "PII_ENCRYPTION_KEY")
	viper.BindEnv("log.file", "LOG_FILE")
	viper.SetDefault("log.file", "./logs/app.log")
	viper.BindEnv("log.max_size_mb", "LOG_MAX_SIZE_MB")
	viper.SetDefault("log.max_size_mb", 100)
	viper.BindEnv("log.max_backups", "LOG_MAX_BACKUPS")
	viper.SetDefault("log.max_backups", 7)
	viper.BindEnv("log.max_age_days", "LOG_MAX_AGE_DAYS")
	viper.SetDefault("log.max_age_days", 30)
	viper.BindEnv("app.env", "APP_ENV")
	viper.SetDefault("app.env", "production")
	viper.BindEnv("app.version", "APP_VERSION")
	viper.SetDefault("app.version", "v1.0.0")
	viper.BindEnv("app.base_url", "APP_BASE_URL")
	viper.SetDefault("app.base_url", "http://localhost:8080")

	viper.BindEnv("session.inactivity_ttl_minutes", "SESSION_INACTIVITY_TTL_MINUTES")
	viper.BindEnv("session.absolute_ttl_minutes", "SESSION_ABSOLUTE_TTL_MINUTES")

	viper.BindEnv("user.default_daily_limit", "USER_DEFAULT_DAILY_LIMIT")
	viper.BindEnv("user.default_single_tx_limit", "USER_DEFAULT_SINGLE_TX_LIMIT")

	viper.SetDefault("user.default_daily_limit", 500000)
	viper.SetDefault("user.default_single_tx_limit", 100000)
}
