// Package config defines EdgeBox's typed, side-effect-free configuration model.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const SupportedSchemaVersion = 1

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	unitPattern       = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.service$`)
)

// SecretFileRef refers to secret material on disk without loading or embedding
// its contents. Its string and slog representations are always redacted.
type SecretFileRef struct {
	path string
}

func NewSecretFileRef(filePath string) SecretFileRef {
	return SecretFileRef{path: filePath}
}

func (r SecretFileRef) Path() string { return r.path }

func (r SecretFileRef) String() string { return "<redacted-secret-file>" }

func (r SecretFileRef) LogValue() slog.Value {
	return slog.StringValue("<redacted-secret-file>")
}

func (r SecretFileRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.path)
}

func (r *SecretFileRef) UnmarshalJSON(data []byte) error {
	var filePath string
	if err := json.Unmarshal(data, &filePath); err != nil {
		return fmt.Errorf("secret file reference: %w", err)
	}
	r.path = filePath
	return nil
}

func (r SecretFileRef) normalized(field string) (SecretFileRef, error) {
	cleaned, err := normalizeLinuxAbsolutePath(field, r.path)
	if err != nil {
		return SecretFileRef{}, err
	}
	return NewSecretFileRef(cleaned), nil
}

// IdentityRef points to the persistent device identity. Device identity is not
// geographic identity and the referenced file is expected to outlive configs.
type IdentityRef struct {
	DeviceIDFile string `json:"device_id_file"`
}

type CredentialRef struct {
	Name string        `json:"name"`
	File SecretFileRef `json:"file"`
}

type ControlConfig struct {
	Endpoint   string        `json:"endpoint"`
	Credential CredentialRef `json:"credential"`
}

type BootstrapConfig struct {
	SchemaVersion int           `json:"schema_version"`
	Identity      IdentityRef   `json:"identity"`
	Control       ControlConfig `json:"control"`
}

func (c BootstrapConfig) String() string {
	return fmt.Sprintf("BootstrapConfig{schema_version:%d identity:configured control_endpoint:configured credential:<redacted>}", c.SchemaVersion)
}

func (c BootstrapConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("schema_version", c.SchemaVersion),
		slog.String("identity", "configured"),
		slog.String("control_endpoint", "configured"),
		slog.String("credential", "<redacted>"),
	)
}

func (c BootstrapConfig) Normalize() (BootstrapConfig, error) {
	c.Control.Endpoint = strings.TrimSpace(c.Control.Endpoint)
	c.Control.Credential.Name = strings.TrimSpace(c.Control.Credential.Name)

	var err error
	c.Identity.DeviceIDFile, err = normalizeLinuxAbsolutePath("identity.device_id_file", c.Identity.DeviceIDFile)
	if err != nil {
		return BootstrapConfig{}, err
	}
	c.Control.Credential.File, err = c.Control.Credential.File.normalized("control.credential.file")
	if err != nil {
		return BootstrapConfig{}, err
	}
	if endpoint, parseErr := url.Parse(c.Control.Endpoint); parseErr == nil && endpoint.Scheme != "" {
		endpoint.Scheme = strings.ToLower(endpoint.Scheme)
		endpoint.Host = strings.ToLower(endpoint.Host)
		if endpoint.Path == "/" {
			endpoint.Path = ""
		}
		c.Control.Endpoint = endpoint.String()
	}
	return c, c.Validate()
}

func (c BootstrapConfig) Validate() error {
	var errs []error
	if c.SchemaVersion != SupportedSchemaVersion {
		errs = append(errs, fmt.Errorf("schema_version: unsupported value %d", c.SchemaVersion))
	}
	if _, err := normalizeLinuxAbsolutePath("identity.device_id_file", c.Identity.DeviceIDFile); err != nil {
		errs = append(errs, err)
	}
	endpoint, err := url.Parse(c.Control.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		errs = append(errs, errors.New("control.endpoint: must be an HTTPS URL with a host and no user information, query, or fragment"))
	}
	if err := validateIdentifier("control.credential.name", c.Control.Credential.Name); err != nil {
		errs = append(errs, err)
	}
	if _, err := c.Control.Credential.File.normalized("control.credential.file"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

type TransportConfig struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Credentials []CredentialRef `json:"credentials,omitempty"`
}

type NetworkOwnership struct {
	OwnerID          string `json:"owner_id"`
	NftablesFamily   string `json:"nftables_family"`
	NftablesTable    string `json:"nftables_table"`
	RoutingTable     uint32 `json:"routing_table"`
	RulePriority     uint32 `json:"rule_priority"`
	PacketMark       uint32 `json:"packet_mark"`
	ObjectNamePrefix string `json:"object_name_prefix"`
}

type ManagementObservationConfig struct {
	UnitName          string `json:"unit_name"`
	StaleAfterSeconds uint32 `json:"stale_after_seconds"`
}

type RuntimeConfig struct {
	SchemaVersion int                         `json:"schema_version"`
	Version       string                      `json:"version"`
	Transport     TransportConfig             `json:"transport"`
	Network       NetworkOwnership            `json:"network"`
	Management    ManagementObservationConfig `json:"management"`
}

func (c RuntimeConfig) String() string {
	return fmt.Sprintf("RuntimeConfig{schema_version:%d version:%q transport:%q credentials:<redacted> network_owner:%q management_unit:%q}", c.SchemaVersion, c.Version, c.Transport.Kind, c.Network.OwnerID, c.Management.UnitName)
}

func (c RuntimeConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("schema_version", c.SchemaVersion),
		slog.String("version", c.Version),
		slog.String("transport", c.Transport.Kind),
		slog.String("credentials", "<redacted>"),
		slog.String("network_owner", c.Network.OwnerID),
		slog.String("management_unit", c.Management.UnitName),
	)
}

func (c RuntimeConfig) Normalize() (RuntimeConfig, error) {
	c.Version = strings.TrimSpace(c.Version)
	c.Transport.Name = strings.TrimSpace(c.Transport.Name)
	c.Transport.Kind = strings.ToLower(strings.TrimSpace(c.Transport.Kind))
	c.Network.OwnerID = strings.TrimSpace(c.Network.OwnerID)
	c.Network.NftablesFamily = strings.ToLower(strings.TrimSpace(c.Network.NftablesFamily))
	c.Network.NftablesTable = strings.TrimSpace(c.Network.NftablesTable)
	c.Network.ObjectNamePrefix = strings.TrimSpace(c.Network.ObjectNamePrefix)
	c.Management.UnitName = strings.TrimSpace(c.Management.UnitName)

	credentials := make([]CredentialRef, len(c.Transport.Credentials))
	for i, credential := range c.Transport.Credentials {
		credentials[i] = credential
		credentials[i].Name = strings.TrimSpace(credential.Name)
		var err error
		credentials[i].File, err = credential.File.normalized(fmt.Sprintf("transport.credentials[%d].file", i))
		if err != nil {
			return RuntimeConfig{}, err
		}
	}
	c.Transport.Credentials = credentials
	return c, c.Validate()
}

func (c RuntimeConfig) Validate() error {
	var errs []error
	if c.SchemaVersion != SupportedSchemaVersion {
		errs = append(errs, fmt.Errorf("schema_version: unsupported value %d", c.SchemaVersion))
	}
	if c.Version == "" {
		errs = append(errs, errors.New("version: is required"))
	}
	if err := validateIdentifier("transport.name", c.Transport.Name); err != nil {
		errs = append(errs, err)
	}
	if err := validateIdentifier("transport.kind", c.Transport.Kind); err != nil {
		errs = append(errs, err)
	}
	seenCredentials := make(map[string]struct{}, len(c.Transport.Credentials))
	for i, credential := range c.Transport.Credentials {
		field := fmt.Sprintf("transport.credentials[%d]", i)
		if err := validateIdentifier(field+".name", credential.Name); err != nil {
			errs = append(errs, err)
		}
		if _, exists := seenCredentials[credential.Name]; exists {
			errs = append(errs, fmt.Errorf("%s.name: duplicate credential name %q", field, credential.Name))
		}
		seenCredentials[credential.Name] = struct{}{}
		if _, err := credential.File.normalized(field + ".file"); err != nil {
			errs = append(errs, err)
		}
	}
	if err := c.Network.Validate(); err != nil {
		errs = append(errs, err)
	}
	if !unitPattern.MatchString(c.Management.UnitName) {
		errs = append(errs, errors.New("management.unit_name: must be a systemd .service unit name"))
	}
	if c.Management.StaleAfterSeconds == 0 {
		errs = append(errs, errors.New("management.stale_after_seconds: must be greater than zero"))
	}
	return errors.Join(errs...)
}

func (o NetworkOwnership) Validate() error {
	var errs []error
	if err := validateIdentifier("network.owner_id", o.OwnerID); err != nil {
		errs = append(errs, err)
	}
	if o.NftablesFamily != "inet" {
		errs = append(errs, errors.New("network.nftables_family: v0.1 requires inet"))
	}
	if o.NftablesTable != "edgebox" {
		errs = append(errs, errors.New("network.nftables_table: v0.1 requires the isolated edgebox table"))
	}
	if o.RoutingTable == 0 || o.RoutingTable >= 253 {
		errs = append(errs, errors.New("network.routing_table: must be between 1 and 252 to avoid reserved tables"))
	}
	if o.RulePriority == 0 || o.RulePriority >= 32766 {
		errs = append(errs, errors.New("network.rule_priority: must be between 1 and 32765"))
	}
	if o.PacketMark == 0 {
		errs = append(errs, errors.New("network.packet_mark: must be non-zero"))
	}
	if err := validateIdentifier("network.object_name_prefix", o.ObjectNamePrefix); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func validateIdentifier(field, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s: must match %s", field, identifierPattern.String())
	}
	return nil
}

func normalizeLinuxAbsolutePath(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%s: must be an absolute Linux path", field)
	}
	cleaned := path.Clean(value)
	if cleaned == "/" {
		return "", fmt.Errorf("%s: must identify a file, not the filesystem root", field)
	}
	return cleaned, nil
}
