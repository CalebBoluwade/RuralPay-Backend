package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/viper"
)

func StructuredLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			t1 := time.Now()

			defer func() {
				attrs := []any{
					slog.String("app", viper.GetString("app.name")),
					slog.String("env", viper.GetString("app.env")),
					slog.String("version", viper.GetString("app.version")),
					slog.String("service", "api"),
					slog.String("Method", r.Method),
					slog.String("RoutePath", r.URL.Path),
					slog.String("Query", r.URL.RawQuery),
					slog.String("RemoteAddr", r.RemoteAddr),
					slog.String("RequestId", middleware.GetReqID(r.Context())),
					slog.Int("Status", ww.Status()),
					slog.Float64("DurationMs", float64(time.Since(t1).Microseconds())/1000),
					slog.Int("bytes_written", ww.BytesWritten()),
				}
				ctx := r.Context()
				if userID, ok := ctx.Value("userID").(string); ok && userID != "" {
					attrs = append(attrs, slog.String("UserID", userID))
				}
				if merchantID, ok := ctx.Value("merchantID").(string); ok && merchantID != "" {
					attrs = append(attrs, slog.String("MerchantID", merchantID))
				}
				log.Info("Completed Request", attrs...)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
