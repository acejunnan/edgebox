package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"edgebox/internal/management"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge-management-render:", err)
		os.Exit(1)
	}
}

func run() error {
	registryPath := flag.String("registry", "", "validated JSON port registry")
	deviceID := flag.String("device-id", "", "device identity to render")
	bastionHost := flag.String("bastion-host", "", "bastion DNS hostname or IP address")
	bastionSSHPort := flag.Uint("bastion-ssh-port", 22, "bastion SSH port")
	tunnelUser := flag.String("tunnel-user", "edge-tunnel", "dedicated bastion tunnel account")
	serviceUser := flag.String("service-user", "edge-management", "unprivileged EdgeBox service account")
	serviceGroup := flag.String("service-group", "edge-management", "EdgeBox service group")
	privateKeyPath := flag.String("private-key-path", "/var/lib/edgebox/management/id_ed25519", "deployed device private-key path")
	knownHostsPath := flag.String("known-hosts-path", "/var/lib/edgebox/management/known_hosts", "deployed pinned known_hosts path")
	clientConfigPath := flag.String("client-config-path", "/etc/edgebox/edge-management.ssh_config", "deployed OpenSSH client-config path")
	localSSHPort := flag.Uint("local-ssh-port", 22, "local EdgeBox sshd target port")
	serverAliveInterval := flag.Uint("server-alive-interval", 30, "OpenSSH server-alive interval in seconds")
	serverAliveCountMax := flag.Uint("server-alive-count-max", 3, "OpenSSH unanswered server-alive limit")
	connectTimeout := flag.Uint("connect-timeout", 15, "OpenSSH connection timeout in seconds")
	devicePublicKeyPath := flag.String("device-public-key", "", "device Ed25519 public-key file")
	bastionHostKeyPath := flag.String("bastion-host-key", "", "pinned bastion Ed25519 host public-key file")
	outputDirectory := flag.String("output", "", "new staging directory for rendered assets")
	flag.Parse()

	for name, value := range map[string]string{
		"registry":          *registryPath,
		"device-id":         *deviceID,
		"bastion-host":      *bastionHost,
		"device-public-key": *devicePublicKeyPath,
		"bastion-host-key":  *bastionHostKeyPath,
		"output":            *outputDirectory,
	} {
		if value == "" {
			return fmt.Errorf("-%s is required", name)
		}
	}
	if *bastionSSHPort > 65535 || *localSSHPort > 65535 {
		return fmt.Errorf("SSH port exceeds supported range")
	}
	if *serverAliveInterval > 65535 || *serverAliveCountMax > 255 || *connectTimeout > 65535 {
		return fmt.Errorf("OpenSSH timing value exceeds supported range")
	}

	registryFile, err := os.Open(*registryPath)
	if err != nil {
		return fmt.Errorf("open port registry: %w", err)
	}
	registry, parseErr := management.ParsePortRegistry(registryFile)
	closeErr := registryFile.Close()
	if parseErr != nil {
		return parseErr
	}
	if closeErr != nil {
		return fmt.Errorf("close port registry: %w", closeErr)
	}
	assignment, ok := registry.Assignment(*deviceID)
	if !ok {
		return fmt.Errorf("device %q has no reviewed port assignment", *deviceID)
	}
	deviceKey, err := readPublicKey(*devicePublicKeyPath)
	if err != nil {
		return fmt.Errorf("read device public key: %w", err)
	}
	hostKey, err := readPublicKey(*bastionHostKeyPath)
	if err != nil {
		return fmt.Errorf("read bastion host key: %w", err)
	}

	artifacts, err := management.Render(management.RenderInput{
		Config: management.RenderConfig{
			DeviceID:            assignment.DeviceID,
			BastionHost:         *bastionHost,
			BastionSSHPort:      uint16(*bastionSSHPort),
			TunnelUser:          *tunnelUser,
			ServiceUser:         *serviceUser,
			ServiceGroup:        *serviceGroup,
			AssignedPort:        assignment.Port,
			PrivateKeyPath:      *privateKeyPath,
			KnownHostsPath:      *knownHostsPath,
			ClientConfigPath:    *clientConfigPath,
			SSHBinaryPath:       "/usr/bin/ssh",
			LocalSSHPort:        uint16(*localSSHPort),
			ServerAliveInterval: uint16(*serverAliveInterval),
			ServerAliveCountMax: uint8(*serverAliveCountMax),
			ConnectTimeout:      uint16(*connectTimeout),
		},
		DevicePublicKey: deviceKey,
		BastionHostKey:  hostKey,
	})
	if err != nil {
		return err
	}
	if err := artifacts.WriteTo(*outputDirectory); err != nil {
		return err
	}
	return nil
}

func readPublicKey(filePath string) (management.PublicKey, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return management.PublicKey{}, err
	}
	return management.ParsePublicKey(strings.TrimSpace(string(data)))
}
