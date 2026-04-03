package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/lumberjack.v2"
)

// piiPattern maps field keys and regex patterns to their masked replacements.
var piiPatterns = []struct {
	re          *regexp.Regexp
	replacement string
}{
	// Nigerian phone numbers (e.g. 08012345678, +2348012345678)
	{regexp.MustCompile(`(\+?234|0)(7[0-9]|8[0-9]|9[0-9])\d{3}(\d{4})`), `${1}${2}***${3}`},
	// Email addresses
	{regexp.MustCompile(`([a-zA-Z0-9._%+\-]{2})[a-zA-Z0-9._%+\-]*(@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,})`), `${1}***${2}`},
	// Card PANs (13–19 digit sequences)
	{regexp.MustCompile(`\b(\d{6})\d{3,9}(\d{4})\b`), `${1}****${2}`},
	// BVN (11-digit Nigerian BVN)
	{regexp.MustCompile(`\b(\d{2})\d{5}(\d{4})\b`), `${1}*****${2}`},
	// Account numbers (10-digit NUBAN)
	{regexp.MustCompile(`\b(\d{4})\d{2}(\d{4})\b`), `${1}**${2}`},
}

func maskPII(s string) string {
	for _, p := range piiPatterns {
		s = p.re.ReplaceAllString(s, p.replacement)
	}
	return s
}

// piiMaskingHandler wraps an slog.Handler and masks PII in all string values.
type piiMaskingHandler struct {
	inner slog.Handler
}

func (h *piiMaskingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *piiMaskingHandler) Handle(ctx context.Context, r slog.Record) error {
	masked := slog.NewRecord(r.Time, r.Level, maskPII(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		masked.AddAttrs(maskAttr(a))
		return true
	})
	return h.inner.Handle(ctx, masked)
}

func (h *piiMaskingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	masked := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		masked[i] = maskAttr(a)
	}
	return &piiMaskingHandler{inner: h.inner.WithAttrs(masked)}
}

func (h *piiMaskingHandler) WithGroup(name string) slog.Handler {
	return &piiMaskingHandler{inner: h.inner.WithGroup(name)}
}

func maskAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, maskPII(a.Value.String()))
	}
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		masked := make([]any, len(attrs))
		for i, ga := range attrs {
			masked[i] = maskAttr(ga)
		}
		return slog.Group(a.Key, masked...)
	}
	return a
}

// RotationConfig controls lumberjack log rotation.
type RotationConfig struct {
	MaxSizeMB  int // max size in MB before rotation
	MaxBackups int // max number of old log files to retain
	MaxAgeDays int // max days to retain old log files
	Compress   bool
}

// New creates a JSON slog.Logger writing to stdout and a rotating log file,
// with PII masking applied to all log records (skipped when dev is true).
func New(logFile string, opts *slog.HandlerOptions, rot RotationConfig, dev bool) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(filepath.Dir(logFile), 0750); err != nil {
		return nil, nil, err
	}

	// Create the log file explicitly with 0600 so it is owner-only regardless
	// of the process umask. lumberjack will reuse the existing file.
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	f.Close()

	rotator := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    rot.MaxSizeMB,
		MaxBackups: rot.MaxBackups,
		MaxAge:     rot.MaxAgeDays,
		Compress:   rot.Compress,
	}

	if opts == nil {
		opts = &slog.HandlerOptions{Level: slog.LevelInfo}
	}

	w := io.MultiWriter(os.Stdout, rotator)
	base := slog.NewJSONHandler(w, opts)
	var handler slog.Handler = base
	if !dev {
		handler = &piiMaskingHandler{inner: base}
	}
	return slog.New(handler), rotator, nil
}

// MaskMessage is a convenience helper for masking ad-hoc strings (e.g. in legacy log.Printf calls).
func MaskMessage(msg string) string {
	return maskPII(strings.TrimSpace(msg))
}
