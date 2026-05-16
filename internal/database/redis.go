package database

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/spf13/viper"
)

// InitRedis initializes a Redis client. When REDIS_SENTINEL_ADDRS and
// REDIS_SENTINEL_MASTER are set it uses Sentinel (HA); otherwise it falls
// back to a standalone connection.
func InitRedis() *redis.Client {
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.db", 0)

	var rdb *redis.Client

	sentinelAddrs := viper.GetString("redis.sentinel.addrs")
	sentinelMaster := viper.GetString("redis.sentinel.master")

	if sentinelAddrs != "" && sentinelMaster != "" {
		addrs := strings.Split(sentinelAddrs, ",")
		rdb = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:       sentinelMaster,
			SentinelAddrs:    addrs,
			SentinelPassword: viper.GetString("redis.sentinel.password"),
			Password:         viper.GetString("redis.password"),
			DB:               viper.GetInt("redis.db"),
			PoolSize:         20,
			MinIdleConns:     5,
		})
		slog.Info("Redis Sentinel mode", "master", sentinelMaster, "sentinels", addrs)
	} else {
		viper.SetDefault("redis.host", "localhost")
		viper.SetDefault("redis.port", "6379")
		addr := viper.GetString("redis.host") + ":" + viper.GetString("redis.port")
		rdb = redis.NewClient(&redis.Options{
			Addr:         addr,
			Password:     viper.GetString("redis.password"),
			DB:           viper.GetInt("redis.db"),
			PoolSize:     20,
			MinIdleConns: 5,
		})
		slog.Info("Redis standalone mode", "addr", addr)
	}

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("Failed to Open Redis Database", "error", err)
		os.Exit(1)
	}

	slog.Info("Redis Connection Established")
	return rdb
}
