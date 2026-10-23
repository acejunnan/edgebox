package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func validBootstrap() BootstrapConfig {
	return BootstrapConfig{
		SchemaVersion: SupportedSchemaVersion,
		Identity:      IdentityRef{DeviceIDFile: " /var/lib/edgebox/identity/device-id "},
		Control: ControlConfig{
			Endpoint: " HTTPS://CONTROL.EXAMPLE.INVALID/ ",
			Credential: CredentialRef{
				Name: " control-auth ",
				File: NewSecretFileRef(" /var/lib/edgebox/credentials/control-token "),
			},
		},
	}
}

func validRuntime() RuntimeConfig {
	return RuntimeConfig{
		SchemaVersion: SupportedSchemaVersion,
		Version:       " config-1 ",
		Transport: TransportConfig{
			Name: " primary ",
			Kind: " TEST-TRANSPORT ",
			Credentials: []CredentialRef{{
				Name: " device-key ",
				File: NewSecretFileRef(" /var/lib/edgebox/credentials/transport-key "),
			}},
		},
		Network: NetworkOwnership{
			OwnerID:          " edgebox-v01 ",
			NftablesFamily:   " INET ",
			NftablesTable:    " edgebox ",
			RoutingTable:     100,
			RulePriority:     10000,
			PacketMark:       1,
			ObjectNamePrefix: " edgebox ",
		},
		Management: ManagementObservationConfig{
			UnitName:          " edge-management.service ",
			StaleAfterSeconds: 90,
		},
	}
}

func TestBootstrapNormalizeAndValidate(t *testing.T) {
	got, err := validBootstrap().Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got.Control.Endpoint != "https://control.example.invalid" {
		t.Fatalf("Endpoint = %q", got.Control.Endpoint)
	}
	if got.Identity.DeviceIDFile != "/var/lib/edgebox/identity/device-id" {
		t.Fatalf("DeviceIDFile = %q", got.Identity.DeviceIDFile)
	}
	if got.Control.Credential.File.Path() != "/var/lib/edgebox/credentials/control-token" {
		t.Fatalf("credential path was not normalized")
	}
}

func TestConfigurationValidationErrorsAreClear(t *testing.T) {
	cfg := validRuntime()
	cfg.SchemaVersion = 99
	cfg.Network.NftablesTable = "unrelated"
	cfg.Network.RoutingTable = 254
	cfg.Management.StaleAfterSeconds = 0
	cfg.Transport.Credentials = append(cfg.Transport.Credentials, cfg.Transport.Credentials[0])

	_, err := cfg.Normalize()
	if err == nil {
		t.Fatal("Normalize() error = nil")
	}
	for _, want := range []string{"schema_version", "nftables_table", "routing_table", "stale_after_seconds", "duplicate credential"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestBootstrapRejectsCredentialsEmbeddedInEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"https://user@example.invalid",
		"https://example.invalid?token=do-not-embed",
		"https://example.invalid#secret",
	} {
		cfg := validBootstrap()
		cfg.Control.Endpoint = endpoint
		if _, err := cfg.Normalize(); err == nil {
			t.Errorf("Normalize() accepted endpoint %q", endpoint)
		}
	}
}

func TestSecretFileRefJSONRoundTrip(t *testing.T) {
	want := NewSecretFileRef("/var/lib/edgebox/credentials/key")
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got SecretFileRef
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Path() != want.Path() {
		t.Fatalf("round trip path = %q, want %q", got.Path(), want.Path())
	}
}

func TestSafeStringRepresentationsRedactCredentialPath(t *testing.T) {
	secretPath := "/do/not/log/private-key"
	bootstrap := validBootstrap()
	bootstrap.Control.Credential.File = NewSecretFileRef(secretPath)
	runtime := validRuntime()
	runtime.Transport.Credentials[0].File = NewSecretFileRef(secretPath)

	for name, rendered := range map[string]string{
		"secret ref": fmt.Sprint(NewSecretFileRef(secretPath)),
		"bootstrap":  fmt.Sprint(bootstrap),
		"runtime":    fmt.Sprint(runtime),
	} {
		if strings.Contains(rendered, secretPath) {
			t.Errorf("%s leaked secret path: %s", name, rendered)
		}
	}
}

func TestUnvalidatedBootstrapRepresentationsDoNotLeakEndpointCredentials(t *testing.T) {
	const endpoint = "https://alice:unvalidated-password@control.invalid/api?token=unvalidated-token#unvalidated-fragment"
	cfg := validBootstrap()
	cfg.Control.Endpoint = endpoint

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.Info("config", slog.Any("bootstrap", cfg))

	for representation, rendered := range map[string]string{
		"fmt":  fmt.Sprint(cfg),
		"slog": output.String(),
	} {
		for _, forbidden := range []string{"alice", "unvalidated-password", "unvalidated-token", "unvalidated-fragment"} {
			if strings.Contains(rendered, forbidden) {
				t.Errorf("%s representation leaked %q: %s", representation, forbidden, rendered)
			}
		}
	}
}
