package management

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func validRenderInput() RenderInput {
	return RenderInput{
		Config: RenderConfig{
			DeviceID:            "lab-device-a",
			BastionHost:         "bastion.lab.invalid",
			BastionSSHPort:      2222,
			TunnelUser:          "edge-tunnel",
			ServiceUser:         "edge-management",
			ServiceGroup:        "edge-management",
			AssignedPort:        22201,
			PrivateKeyPath:      "/var/lib/edgebox/management/id_ed25519",
			KnownHostsPath:      "/var/lib/edgebox/management/known_hosts",
			ClientConfigPath:    "/etc/edgebox/edge-management.ssh_config",
			SSHBinaryPath:       "/usr/bin/ssh",
			LocalSSHPort:        22,
			ServerAliveInterval: 30,
			ServerAliveCountMax: 3,
			ConnectTimeout:      15,
		},
		DevicePublicKey: testEd25519PublicKey(1),
		BastionHostKey:  testEd25519PublicKey(33),
	}
}

func TestRenderedArtifactsMatchReviewedGoldenFiles(t *testing.T) {
	artifacts, err := Render(validRenderInput())
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{
		"edge-management.service",
		"edge-management.ssh_config",
		"edge-tunnel-sshd.conf",
		"known_hosts",
		"tunnel-authorized_keys",
	}
	if got := artifactNames(artifacts.Files()); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("artifact names = %v", got)
	}
	for _, artifact := range artifacts.Files() {
		goldenPath := filepath.Join("testdata", artifact.Name+".golden")
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(artifact.Content) != string(want) {
			t.Errorf("artifact %s differs from reviewed golden file\n--- got ---\n%s\n--- want ---\n%s", artifact.Name, artifact.Content, want)
		}
	}
}

func TestRenderingIsDeterministicAndLoopbackOnly(t *testing.T) {
	first, err := Render(validRenderInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(validRenderInput())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Files(), second.Files()) {
		t.Fatal("rendering is not deterministic")
	}
	for _, artifact := range first.Files() {
		content := string(artifact.Content)
		if strings.Contains(content, "0.0.0.0") || strings.Contains(content, "*:22201") {
			t.Errorf("artifact %s contains wildcard reverse binding: %s", artifact.Name, content)
		}
	}
	authorized := artifactContent(t, first, "tunnel-authorized_keys")
	if !strings.Contains(authorized, `permitlisten="127.0.0.1:22201"`) || strings.Contains(authorized, "22202") {
		t.Fatalf("authorized key is not restricted to assigned listener: %s", authorized)
	}
	client := artifactContent(t, first, "edge-management.ssh_config")
	for _, required := range []string{
		"StrictHostKeyChecking yes",
		"UserKnownHostsFile /var/lib/edgebox/management/known_hosts",
		"RemoteForward 127.0.0.1:22201 127.0.0.1:22",
		"BatchMode yes",
		"ExitOnForwardFailure yes",
	} {
		if !strings.Contains(client, required) {
			t.Errorf("client configuration missing %q", required)
		}
	}
}

func TestRenderAcceptsLiteralIPv6BastionAndPinsItsPort(t *testing.T) {
	input := validRenderInput()
	input.Config.BastionHost = "2001:db8::10"
	artifacts, err := Render(input)
	if err != nil {
		t.Fatal(err)
	}
	client := artifactContent(t, artifacts, "edge-management.ssh_config")
	if !strings.Contains(client, "HostName 2001:db8::10") {
		t.Fatalf("client configuration does not preserve IPv6 host: %s", client)
	}
	knownHosts := artifactContent(t, artifacts, "known_hosts")
	if !strings.HasPrefix(knownHosts, "[2001:db8::10]:2222 ssh-ed25519 ") {
		t.Fatalf("known_hosts does not pin bracketed IPv6 host and port: %s", knownHosts)
	}
}

func TestSystemdServiceIsUnprivilegedAndIndependent(t *testing.T) {
	artifacts, err := Render(validRenderInput())
	if err != nil {
		t.Fatal(err)
	}
	unit := artifactContent(t, artifacts, "edge-management.service")
	for _, required := range []string{
		"User=edge-management",
		"Group=edge-management",
		"Restart=always",
		"CapabilityBoundingSet=\n",
		"AmbientCapabilities=\n",
		"NoNewPrivileges=yes",
	} {
		if !strings.Contains(unit, required) {
			t.Errorf("systemd unit missing %q", required)
		}
	}
	for _, forbidden := range []string{"edge-agent.service", "network-online.target", "CAP_NET_ADMIN", "ExecStartPre=/usr/bin/systemctl"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("systemd unit contains forbidden coupling %q", forbidden)
		}
	}
}

func TestBastionSSHDRestrictionsAreAccountScoped(t *testing.T) {
	artifacts, err := Render(validRenderInput())
	if err != nil {
		t.Fatal(err)
	}
	fragment := artifactContent(t, artifacts, "edge-tunnel-sshd.conf")
	global, matchBlock, found := strings.Cut(fragment, "Match User edge-tunnel\n")
	if !found {
		t.Fatalf("bastion fragment has no tunnel-account Match block: %s", fragment)
	}
	for _, setting := range []string{
		"PasswordAuthentication",
		"KbdInteractiveAuthentication",
		"PubkeyAuthentication",
		"GatewayPorts",
	} {
		if strings.Contains(global, setting) {
			t.Errorf("bastion fragment sets %s globally: %s", setting, global)
		}
	}
	for _, required := range []string{
		"    AuthenticationMethods publickey\n",
		"    PubkeyAuthentication yes\n",
		"    PasswordAuthentication no\n",
		"    KbdInteractiveAuthentication no\n",
		"    GatewayPorts no\n",
		"    AllowTcpForwarding remote\n",
		"    AllowStreamLocalForwarding no\n",
		"    AllowAgentForwarding no\n",
		"    X11Forwarding no\n",
		"    PermitTTY no\n",
		"    PermitTunnel no\n",
		"    PermitOpen none\n",
		"    MaxSessions 0\n",
		"    ClientAliveInterval 30\n",
		"    ClientAliveCountMax 2\n",
		"    ForceCommand /bin/false\n",
	} {
		if !strings.Contains(matchBlock, required) {
			t.Errorf("tunnel-account Match block missing %q: %s", required, matchBlock)
		}
	}
}

func TestRenderRejectsConfigurationInjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RenderInput)
	}{
		{name: "device newline", mutate: func(i *RenderInput) { i.Config.DeviceID = "device\ncommand" }},
		{name: "username option", mutate: func(i *RenderInput) { i.Config.TunnelUser = "-oProxyCommand" }},
		{name: "username whitespace", mutate: func(i *RenderInput) { i.Config.ServiceUser = "edge management" }},
		{name: "hostname newline", mutate: func(i *RenderInput) { i.Config.BastionHost = "host.invalid\nProxyCommand=x" }},
		{name: "hostname wildcard", mutate: func(i *RenderInput) { i.Config.BastionHost = "*.invalid" }},
		{name: "private key missing", mutate: func(i *RenderInput) { i.Config.PrivateKeyPath = "" }},
		{name: "relative path", mutate: func(i *RenderInput) { i.Config.KnownHostsPath = "known_hosts" }},
		{name: "path traversal", mutate: func(i *RenderInput) { i.Config.ClientConfigPath = "/etc/edgebox/../ssh/evil" }},
		{name: "path newline", mutate: func(i *RenderInput) { i.Config.SSHBinaryPath = "/usr/bin/ssh\ncommand" }},
		{name: "privileged assigned port", mutate: func(i *RenderInput) { i.Config.AssignedPort = 22 }},
		{name: "missing host trust", mutate: func(i *RenderInput) { i.BastionHostKey = PublicKey{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validRenderInput()
			test.mutate(&input)
			if _, err := Render(input); err == nil {
				t.Fatal("Render() accepted unsafe input")
			}
		})
	}
}

func TestArtifactWriteModesAndNoSecretContents(t *testing.T) {
	artifacts, err := Render(validRenderInput())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := artifacts.WriteTo(directory); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts.Files() {
		path := filepath.Join(directory, artifact.Name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "PRIVATE KEY") || strings.Contains(string(data), "password=") {
			t.Errorf("artifact %s contains secret material", artifact.Name)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != artifact.Mode {
				t.Errorf("artifact %s mode = %o, want %o", artifact.Name, info.Mode().Perm(), artifact.Mode)
			}
		}
	}
	if err := artifacts.WriteTo(directory); err == nil {
		t.Fatal("WriteTo() replaced existing deployment artifacts")
	}
}

func testEd25519PublicKey(start byte) PublicKey {
	blob := make([]byte, 0, 4+11+4+32)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len("ssh-ed25519")))
	blob = append(blob, "ssh-ed25519"...)
	blob = binary.BigEndian.AppendUint32(blob, 32)
	for offset := byte(0); offset < 32; offset++ {
		blob = append(blob, start+offset)
	}
	return PublicKey{Type: "ssh-ed25519", Data: base64.StdEncoding.EncodeToString(blob)}
}

func artifactContent(t *testing.T, artifacts Artifacts, name string) string {
	t.Helper()
	for _, artifact := range artifacts.Files() {
		if artifact.Name == name {
			return string(artifact.Content)
		}
	}
	t.Fatalf("artifact %q not found", name)
	return ""
}
