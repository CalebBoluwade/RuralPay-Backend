package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
)

// HealthHandler handles health check endpoints
type HealthHandler struct {
	db    *sql.DB
	redis *redis.Client
}

// HealthStatus represents the overall health status
type HealthStatus struct {
	Status    string                   `json:"status"`
	Timestamp time.Time                `json:"timestamp"`
	Services  map[string]ServiceStatus `json:"services"`
}

// ServiceStatus represents individual service health status
type ServiceStatus struct {
	Status  string `json:"status"`
	Latency string `json:"latency"`
	Error   string `json:"error,omitempty"`
}

// NewHealthHandler creates a new health handler
func NewHealthHandler(db *sql.DB, redisClient *redis.Client) *HealthHandler {
	return &HealthHandler{
		db:    db,
		redis: redisClient,
	}
}

// HealthCheck performs a comprehensive health check on all dependencies
// @Summary Deep Health Check
// @Description Checks the health of database and Redis connections
// @Tags health
// @Produce json
// @Success 200 {object} HealthStatus
// @Failure 503 {object} HealthStatus
// @Router /health [get]
func (h *HealthHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status := HealthStatus{
		Timestamp: time.Now(),
		Services:  make(map[string]ServiceStatus),
	}

	// Check database
	dbStatus := h.checkDatabase(ctx)
	status.Services["database"] = dbStatus

	// Check Redis
	redisStatus := h.checkRedis(ctx)
	status.Services["redis"] = redisStatus

	// Determine overall status
	overallHealthy := dbStatus.Status == "healthy" && redisStatus.Status == "healthy"
	if overallHealthy {
		status.Status = "healthy"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	} else {
		status.Status = "unhealthy"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(status)
}

// checkDatabase checks the database connection health
func (h *HealthHandler) checkDatabase(ctx context.Context) ServiceStatus {
	start := time.Now()
	status := ServiceStatus{}

	if h.db == nil {
		status.Status = "unhealthy"
		status.Error = "database connection not initialized"
		return status
	}

	// Ping the database
	if err := h.db.PingContext(ctx); err != nil {
		status.Status = "unhealthy"
		status.Error = err.Error()
		slog.Error("health.database.ping_failed", "error", err)
	} else {
		status.Status = "healthy"
	}

	status.Latency = time.Since(start).String()
	return status
}

// checkRedis checks the Redis connection health
func (h *HealthHandler) checkRedis(ctx context.Context) ServiceStatus {
	start := time.Now()
	status := ServiceStatus{}

	if h.redis == nil {
		status.Status = "degraded"
		status.Error = "Redis client not initialized"
		return status
	}

	// Ping Redis
	if err := h.redis.Ping(ctx).Err(); err != nil {
		status.Status = "unhealthy"
		status.Error = err.Error()
		slog.Error("health.redis.ping_failed", "error", err)
	} else {
		status.Status = "healthy"
	}

	status.Latency = time.Since(start).String()
	return status
}
