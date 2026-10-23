package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"edgebox/internal/config"
)

func TestLoggerRedactsSecretsAndCredentialReferences(t *testing.T) {
	const (
		privateKey = "PRIVATE-KEY-CONTENT-MUST-NOT-APPEAR"
		token      = "TOKEN-CONTENT-MUST-NOT-APPEAR"
		secretPath = "/var/lib/edgebox/credentials/private-key"
	)
	var output bytes.Buffer
	logger := NewJSON(&output, slog.LevelDebug)
	runtimeConfig := config.RuntimeConfig{
		SchemaVersion: 1,
		Version:       "config-1",
		Transport: config.TransportConfig{
			Name: "primary",
			Kind: "test-transport",
			Credentials: []config.CredentialRef{{
				Name: "device-key",
				File: config.NewSecretFileRef(secretPath),
			}},
		},
	}
	logger.Info("redaction test",
		slog.String("private_key", privateKey),
		slog.String("auth-token", token),
		slog.Any("opaque", Secret(privateKey)),
		slog.Any("runtime_config", runtimeConfig),
	)

	logged := output.String()
	for _, forbidden := range []string{privateKey, token, secretPath} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked %q: %s", forbidden, logged)
		}
	}
	if count := strings.Count(logged, redacted); count < 4 {
		t.Fatalf("log did not contain expected redactions: %s", logged)
	}
}

func TestWithAddsOperationalContext(t *testing.T) {
	var output bytes.Buffer
	logger := With(NewJSON(&output, slog.LevelInfo), Fields{
		DeviceID:      "device-001",
		Component:     "agent",
		Event:         "startup",
		Transport:     "unconfigured",
		ConfigVersion: "config-1",
		ErrorCode:     "none",
	})
	logger.Info("started")
	logged := output.String()
	for _, want := range []string{"device-001", "agent", "startup", "unconfigured", "config-1", "none"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log does not contain %q: %s", want, logged)
		}
	}
}

func TestSensitiveParentGroupRedactsNestedValues(t *testing.T) {
	const sensitiveValue = "nested-sensitive-value-must-not-appear"
	var output bytes.Buffer
	logger := NewJSON(&output, slog.LevelInfo)
	logger.Info("nested redaction",
		slog.Group("credential",
			slog.Group("metadata",
				slog.String("path", sensitiveValue),
				slog.String("format", sensitiveValue),
			),
		),
	)
	logged := output.String()
	if strings.Contains(logged, sensitiveValue) {
		t.Fatalf("sensitive parent group leaked nested values: %s", logged)
	}
	if count := strings.Count(logged, redacted); count != 2 {
		t.Fatalf("nested sensitive group redaction count = %d, log = %s", count, logged)
	}
}
