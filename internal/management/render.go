package management

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const clientAlias = "edgebox-management-bastion"

var (
	usernamePattern  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hostLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	linuxPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
)

type PublicKey struct {
	Type string
	Data string
}

type RenderConfig struct {
	DeviceID            string
	BastionHost         string
	BastionSSHPort      uint16
	TunnelUser          string
	ServiceUser         string
	ServiceGroup        string
	AssignedPort        uint16
	PrivateKeyPath      string
	KnownHostsPath      string
	ClientConfigPath    string
	SSHBinaryPath       string
	LocalSSHPort        uint16
	ServerAliveInterval uint16
	ServerAliveCountMax uint8
	ConnectTimeout      uint16
}

type RenderInput struct {
	Config          RenderConfig
	DevicePublicKey PublicKey
	BastionHostKey  PublicKey
}

type Artifact struct {
	Name    string
	Mode    os.FileMode
	Content []byte
}

type Artifacts struct {
	files []Artifact
}

func Render(input RenderInput) (Artifacts, error) {
	if err := input.Config.Validate(); err != nil {
		return Artifacts{}, err
	}
	if err := validateEd25519PublicKey("device public key", input.DevicePublicKey); err != nil {
		return Artifacts{}, err
	}
	if err := validateEd25519PublicKey("bastion host key", input.BastionHostKey); err != nil {
		return Artifacts{}, err
	}

	config := input.Config
	files := []Artifact{
		{Name: "edge-management.ssh_config", Mode: 0o600, Content: []byte(renderClientConfig(config))},
		{Name: "edge-management.service", Mode: 0o644, Content: []byte(renderSystemdService(config))},
		{Name: "tunnel-authorized_keys", Mode: 0o600, Content: []byte(renderAuthorizedKey(config, input.DevicePublicKey))},
		{Name: "edge-tunnel-sshd.conf", Mode: 0o644, Content: []byte(renderBastionSSHD(config))},
		{Name: "known_hosts", Mode: 0o644, Content: []byte(renderKnownHosts(config, input.BastionHostKey))},
	}
	return Artifacts{files: files}, nil
}

func (c RenderConfig) Validate() error {
	var errs []error
	if !deviceIDPattern.MatchString(c.DeviceID) {
		errs = append(errs, errors.New("device_id: invalid device ID"))
	}
	if !validHost(c.BastionHost) {
		errs = append(errs, errors.New("bastion_host: invalid hostname or IP address"))
	}
	if c.BastionSSHPort == 0 {
		errs = append(errs, errors.New("bastion_ssh_port: must be non-zero"))
	}
	for field, value := range map[string]string{
		"tunnel_user":   c.TunnelUser,
		"service_user":  c.ServiceUser,
		"service_group": c.ServiceGroup,
	} {
		if !usernamePattern.MatchString(value) {
			errs = append(errs, fmt.Errorf("%s: invalid account name", field))
		}
	}
	if c.AssignedPort < 1024 {
		errs = append(errs, errors.New("assigned_port: must be an unprivileged TCP port"))
	}
	for field, value := range map[string]string{
		"private_key_path":   c.PrivateKeyPath,
		"known_hosts_path":   c.KnownHostsPath,
		"client_config_path": c.ClientConfigPath,
		"ssh_binary_path":    c.SSHBinaryPath,
	} {
		if !validLinuxFilePath(value) {
			errs = append(errs, fmt.Errorf("%s: must be a normalized absolute Linux file path", field))
		}
	}
	if c.LocalSSHPort == 0 {
		errs = append(errs, errors.New("local_ssh_port: must be non-zero"))
	}
	if c.ServerAliveInterval == 0 {
		errs = append(errs, errors.New("server_alive_interval: must be non-zero"))
	}
	if c.ServerAliveCountMax == 0 {
		errs = append(errs, errors.New("server_alive_count_max: must be non-zero"))
	}
	if c.ConnectTimeout == 0 || c.ConnectTimeout > 60 {
		errs = append(errs, errors.New("connect_timeout: must be between 1 and 60 seconds"))
	}
	return errors.Join(errs...)
}

func (a Artifacts) Files() []Artifact {
	files := make([]Artifact, len(a.files))
	for index, file := range a.files {
		files[index] = file
		files[index].Content = append([]byte(nil), file.Content...)
	}
	return files
}

// WriteTo writes fixed-name deployment assets to a caller-selected staging
// directory. It refuses to replace existing files and never writes to /etc.
func (a Artifacts) WriteTo(directory string) error {
	if directory == "" {
		return errors.New("artifact output directory is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create artifact output directory: %w", err)
	}
	for _, artifact := range a.files {
		target := filepath.Join(directory, artifact.Name)
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, artifact.Mode)
		if err != nil {
			return fmt.Errorf("create artifact %q: %w", artifact.Name, err)
		}
		if _, err := file.Write(artifact.Content); err != nil {
			_ = file.Close()
			return fmt.Errorf("write artifact %q: %w", artifact.Name, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close artifact %q: %w", artifact.Name, err)
		}
	}
	return nil
}

func ParsePublicKey(line string) (PublicKey, error) {
	if strings.ContainsAny(line, "\r\n") {
		return PublicKey{}, errors.New("public key must contain exactly one line")
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return PublicKey{}, errors.New("public key must contain key type and data")
	}
	key := PublicKey{Type: fields[0], Data: fields[1]}
	if err := validateEd25519PublicKey("public key", key); err != nil {
		return PublicKey{}, err
	}
	return key, nil
}

func renderClientConfig(config RenderConfig) string {
	return fmt.Sprintf(`Host %s
    HostName %s
    Port %d
    User %s
    IdentityFile %s
    IdentitiesOnly yes
    IdentityAgent none
    AddKeysToAgent no
    UserKnownHostsFile %s
    GlobalKnownHostsFile /dev/null
    StrictHostKeyChecking yes
    BatchMode yes
    ExitOnForwardFailure yes
    ServerAliveInterval %d
    ServerAliveCountMax %d
    ConnectTimeout %d
    TCPKeepAlive yes
    RequestTTY no
    ForwardAgent no
    ForwardX11 no
    RemoteForward 127.0.0.1:%d 127.0.0.1:%d
`, clientAlias, config.BastionHost, config.BastionSSHPort, config.TunnelUser, config.PrivateKeyPath, config.KnownHostsPath, config.ServerAliveInterval, config.ServerAliveCountMax, config.ConnectTimeout, config.AssignedPort, config.LocalSSHPort)
}

func renderSystemdService(config RenderConfig) string {
	return fmt.Sprintf(`[Unit]
Description=EdgeBox outbound reverse SSH management tunnel
Wants=network.target
After=network.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=%s
Group=%s
UMask=0077
ExecStart=%s -N -T -F %s %s
Restart=always
RestartSec=10s
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
AmbientCapabilities=
ReadOnlyPaths=%s %s %s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, config.ServiceUser, config.ServiceGroup, config.SSHBinaryPath, config.ClientConfigPath, clientAlias, config.ClientConfigPath, config.PrivateKeyPath, config.KnownHostsPath)
}

func renderAuthorizedKey(config RenderConfig, key PublicKey) string {
	return fmt.Sprintf(`restrict,port-forwarding,permitlisten="127.0.0.1:%d" %s %s %s
`, config.AssignedPort, key.Type, key.Data, config.DeviceID)
}

func renderBastionSSHD(config RenderConfig) string {
	return fmt.Sprintf(`# EdgeBox tunnel-establishment account. Operator accounts are separate.
Match User %s
    AuthenticationMethods publickey
    PubkeyAuthentication yes
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    GatewayPorts no
    AllowTcpForwarding remote
    AllowStreamLocalForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
    PermitTunnel no
    PermitOpen none
    MaxSessions 0
    ClientAliveInterval 30
    ClientAliveCountMax 2
    ForceCommand /bin/false
`, config.TunnelUser)
}

func renderKnownHosts(config RenderConfig, key PublicKey) string {
	return fmt.Sprintf("%s %s %s\n", knownHostName(config.BastionHost, config.BastionSSHPort), key.Type, key.Data)
}

func knownHostName(host string, port uint16) string {
	if port == 22 {
		return host
	}
	return fmt.Sprintf("[%s]:%d", host, port)
}

func validHost(host string) bool {
	if host == "" || strings.ContainsAny(host, "\r\n[]*?\\/ ") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if !hostLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func validLinuxFilePath(filePath string) bool {
	return filePath != "" && linuxPathPattern.MatchString(filePath) && path.IsAbs(filePath) && path.Clean(filePath) == filePath && filePath != "/"
}

func validateEd25519PublicKey(field string, key PublicKey) error {
	if key.Type != "ssh-ed25519" {
		return fmt.Errorf("%s: only ssh-ed25519 is supported", field)
	}
	blob, err := base64.StdEncoding.DecodeString(key.Data)
	if err != nil {
		return fmt.Errorf("%s: invalid base64 data", field)
	}
	keyType, remainder, ok := readSSHString(blob)
	if !ok || string(keyType) != key.Type {
		return fmt.Errorf("%s: key blob type does not match %s", field, key.Type)
	}
	publicKey, trailing, ok := readSSHString(remainder)
	if !ok || len(publicKey) != 32 || len(trailing) != 0 {
		return fmt.Errorf("%s: invalid Ed25519 public key blob", field)
	}
	return nil
}

func readSSHString(data []byte) (value, remainder []byte, ok bool) {
	if len(data) < 4 {
		return nil, nil, false
	}
	length := uint64(binary.BigEndian.Uint32(data[:4]))
	if length > uint64(len(data)-4) {
		return nil, nil, false
	}
	end := 4 + int(length)
	return data[4:end], data[end:], true
}

func artifactNames(files []Artifact) []string {
	names := make([]string, len(files))
	for index, file := range files {
		names[index] = file.Name
	}
	sort.Strings(names)
	return names
}
