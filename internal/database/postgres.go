package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/viper"
)

var db *sql.DB

// DBConfig holds database configuration
type DBConfig struct {
	Host            string
	Port            string
	User            string
	Password        string
	Name            string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// GetConfig returns database configuration with defaults
func GetConfig() *DBConfig {
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", "5432")
	viper.SetDefault("database.user", "postgres")
	viper.SetDefault("database.password", "")
	viper.SetDefault("database.name", "ruralpay")
	viper.SetDefault("database.ssl_mode", "disable")
	viper.SetDefault("database.max_open_conns", 25)
	viper.SetDefault("database.max_idle_conns", 5)
	viper.SetDefault("database.conn_max_lifetime", time.Minute*5)

	return &DBConfig{
		Host:            viper.GetString("database.host"),
		Port:            viper.GetString("database.port"),
		User:            viper.GetString("database.user"),
		Password:        viper.GetString("database.password"),
		Name:            viper.GetString("database.name"),
		SSLMode:         viper.GetString("database.ssl_mode"),
		MaxOpenConns:    viper.GetInt("database.max_open_conns"),
		MaxIdleConns:    viper.GetInt("database.max_idle_conns"),
		ConnMaxLifetime: viper.GetDuration("database.conn_max_lifetime"),
	}
}

const (
	maxRetries   = 5
	initialDelay = 2 * time.Second
	maxDelay     = 30 * time.Second
)

func connectWithRetry(database *sql.DB) error {
	delay := initialDelay
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := database.Ping(); err == nil {
			return nil
		} else if attempt == maxRetries {
			return fmt.Errorf("database unreachable after %d attempts: %w", maxRetries, err)
		}
		slog.Warn("Database ping failed, retrying", "attempt", attempt, "backoff", delay)
		time.Sleep(delay)
		delay = min(delay*2, maxDelay)
	}
	return nil
}

// InitDB initializes the database connection
func InitDB() (*sql.DB, error) {
	config := GetConfig()

	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		config.Host, config.Port, config.User, config.Password, config.Name, config.SSLMode,
	)

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		slog.Error("Failed to open database", "error", err)
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err = connectWithRetry(db); err != nil {
		slog.Error("Failed to connect to database", "error", err)
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	db.SetConnMaxIdleTime(2 * time.Minute)

	slog.Info("Database connection established")
	return db, nil
}

// GetDB returns the database connection
func GetDB() *sql.DB {
	return db
}

// CloseDB closes the database connection
func CloseDB() error {
	if db != nil {
		return db.Close()
	}
	return nil
}
