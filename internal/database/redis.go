package database

import (
	"context"
	"log/slog"
	"os"

	"github.com/go-redis/redis/v8"
	"github.com/spf13/viper"
)

// InitRedis initializes Redis client with config
func InitRedis() *redis.Client {
	viper.SetDefault("redis.host", "localhost")
	viper.SetDefault("redis.port", "6379")
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.db", 0)

	addr := viper.GetString("redis.host") + ":" + viper.GetString("redis.port")
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     viper.GetString("redis.password"),
		DB:           viper.GetInt("redis.db"),
		PoolSize:     20,
		MinIdleConns: 5,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("Failed to Open Redis Database", "error", err)
		os.Exit(1)
	}

	slog.Info("Redis Connection Established")
	return rdb
}
