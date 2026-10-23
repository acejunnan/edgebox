// Package logging constructs structured slog loggers with conservative
// redaction of sensitive attributes.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

const redacted = "<redacted>"

// Secret protects sensitive values even if a caller uses a non-sensitive key.
type Secret string

func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

func NewJSON(output io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactAttribute,
	}))
}

type Fields struct {
	DeviceID      string
	Component     string
	Event         string
	Transport     string
	Relay         string
	ConfigVersion string
	ErrorCode     string
}

func With(logger *slog.Logger, fields Fields) *slog.Logger {
	attributes := make([]any, 0, 14)
	attributes = appendString(attributes, "device_id", fields.DeviceID)
	attributes = appendString(attributes, "component", fields.Component)
	attributes = appendString(attributes, "event", fields.Event)
	attributes = appendString(attributes, "transport", fields.Transport)
	attributes = appendString(attributes, "relay", fields.Relay)
	attributes = appendString(attributes, "config_version", fields.ConfigVersion)
	attributes = appendString(attributes, "error", fields.ErrorCode)
	return logger.With(attributes...)
}

func redactAttribute(groups []string, attribute slog.Attr) slog.Attr {
	if isSensitiveKey(attribute.Key) || containsSensitiveGroup(groups) {
		attribute.Value = slog.StringValue(redacted)
	}
	return attribute
}

func containsSensitiveGroup(groups []string) bool {
	for _, group := range groups {
		if isSensitiveKey(group) {
			return true
		}
	}
	return false
}

func isSensitiveKey(key string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(key))
	for _, sensitive := range []string{"password", "passwd", "token", "secret", "privatekey", "presharedkey", "credential"} {
		if strings.Contains(normalized, sensitive) {
			return true
		}
	}
	return false
}

func appendString(attributes []any, key, value string) []any {
	if value == "" {
		return attributes
	}
	return append(attributes, slog.String(key, value))
}
