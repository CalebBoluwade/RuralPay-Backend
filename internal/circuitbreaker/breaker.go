package circuitbreaker

import (
	"log/slog"
	"sync"
	"time"

	"github.com/sony/gobreaker"
)

// Registry holds named circuit breakers shared across the application.
type Registry struct {
	mu       sync.RWMutex
	breakers map[string]*gobreaker.CircuitBreaker
}

var global = &Registry{breakers: make(map[string]*gobreaker.CircuitBreaker)}

// Get returns the named breaker from the global registry, creating it with
// the provided settings on first access.
func Get(name string, settings gobreaker.Settings) *gobreaker.CircuitBreaker {
	global.mu.RLock()
	cb, ok := global.breakers[name]
	global.mu.RUnlock()
	if ok {
		return cb
	}

	global.mu.Lock()
	defer global.mu.Unlock()
	// Double-check after acquiring write lock.
	if cb, ok = global.breakers[name]; ok {
		return cb
	}
	settings.Name = name
	settings.OnStateChange = func(name string, from, to gobreaker.State) {
		slog.Warn("circuit_breaker.state_change", "breaker", name, "from", from.String(), "to", to.String())
	}
	cb = gobreaker.NewCircuitBreaker(settings)
	global.breakers[name] = cb
	return cb
}

// NIBSSSettlementSettings returns the standard settings for the NIBSS settlement breaker.
func NIBSSSettlementSettings() gobreaker.Settings {
	return gobreaker.Settings{
		MaxRequests: 1,
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.Requests >= 5 &&
				float64(counts.TotalFailures)/float64(counts.Requests) >= 0.6
		},
	}
}

// NIBSSBVNSettings returns the standard settings for the NIBSS BVN breaker.
func NIBSSBVNSettings() gobreaker.Settings {
	return gobreaker.Settings{
		MaxRequests: 1,
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.Requests >= 3 &&
				float64(counts.TotalFailures)/float64(counts.Requests) >= 0.6
		},
	}
}

// NIBSSMandateSettings returns the standard settings for the NIBSS mandate breaker.
func NIBSSMandateSettings() gobreaker.Settings {
	return gobreaker.Settings{
		MaxRequests: 1,
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.Requests >= 5 &&
				float64(counts.TotalFailures)/float64(counts.Requests) >= 0.6
		},
	}
}
