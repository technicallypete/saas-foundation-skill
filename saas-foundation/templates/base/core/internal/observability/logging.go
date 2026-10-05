// Package observability provides structured, redacted logging and request
// and trace correlation. Logs never carry secret values or credential fields.
package observability

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

var sensitiveKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|api_?key|authorization|cookie|dsn|credential)`)

// NewLogger returns a JSON logger that redacts sensitive attribute keys and
// any occurrence of the given secret values.
func NewLogger(w io.Writer, level slog.Level, secrets []string) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(&redactingHandler{next: base, secrets: nonEmpty(secrets)})
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

type redactingHandler struct {
	next    slog.Handler
	secrets []string
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redact(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.redact(a)
	}
	return &redactingHandler{next: h.next.WithAttrs(clean), secrets: h.secrets}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), secrets: h.secrets}
}

func (h *redactingHandler) redact(a slog.Attr) slog.Attr {
	if sensitiveKey.MatchString(a.Key) {
		return slog.String(a.Key, redacted)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		clean := make([]any, 0, len(group))
		for _, g := range group {
			clean = append(clean, h.redact(g))
		}
		return slog.Group(a.Key, clean...)
	case slog.KindString:
		return slog.String(a.Key, h.scrub(v.String()))
	default:
		if s := v.String(); h.containsSecret(s) {
			return slog.String(a.Key, h.scrub(s))
		}
		return slog.Attr{Key: a.Key, Value: v}
	}
}

func (h *redactingHandler) containsSecret(s string) bool {
	for _, sec := range h.secrets {
		if strings.Contains(s, sec) {
			return true
		}
	}
	return false
}

func (h *redactingHandler) scrub(s string) string {
	for _, sec := range h.secrets {
		s = strings.ReplaceAll(s, sec, redacted)
	}
	return s
}
