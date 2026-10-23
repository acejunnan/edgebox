package updater

import (
	"errors"
	"strings"
)

// ValidateServiceAsset permits only an unprivileged EdgeBox application unit,
// with a fixed executable and no additional command hooks or capabilities.
func ValidateServiceAsset(name string, data []byte) error {
	if name != "edge-web" && name != "edge-agent" {
		return errors.New("unsupported application service")
	}
	if len(data) > 8192 {
		return errors.New("unit too large")
	}
	allowed := map[string]bool{"Description": true, "After": true, "Wants": true, "StartLimitIntervalSec": true, "Type": true, "User": true, "Group": true, "ExecStart": true, "Restart": true, "RestartSec": true, "UMask": true, "NoNewPrivileges": true, "PrivateTmp": true, "PrivateDevices": true, "ProtectSystem": true, "ProtectHome": true, "ProtectKernelTunables": true, "ProtectKernelModules": true, "ProtectControlGroups": true, "RestrictAddressFamilies": true, "CapabilityBoundingSet": true, "AmbientCapabilities": true, "ReadWritePaths": true, "MemoryMax": true, "StandardOutput": true, "StandardError": true, "WantedBy": true}
	values := map[string]string{}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "[Unit]" || line == "[Service]" || line == "[Install]" {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !allowed[key] {
			return errors.New("unapproved unit directive")
		}
		expectedSection := "[Service]"
		if key == "Description" || key == "After" || key == "Wants" || key == "StartLimitIntervalSec" {
			expectedSection = "[Unit]"
		} else if key == "WantedBy" {
			expectedSection = "[Install]"
		}
		if section != expectedSection {
			return errors.New("unit directive in incorrect section")
		}
		if _, exists := values[key]; exists {
			return errors.New("duplicate unit directive")
		}
		values[key] = value
	}
	expected := "/opt/edgebox/current/bin/" + name
	if name == "edge-web" {
		expected += " serve"
	}
	if name == "edge-agent" {
		expected += " --state-dir /var/lib/edgebox/agent"
	}
	if values["User"] != name || values["Group"] != name || values["ExecStart"] != expected || values["NoNewPrivileges"] != "yes" || values["ProtectSystem"] != "strict" || values["CapabilityBoundingSet"] != "" || values["AmbientCapabilities"] != "" {
		return errors.New("unsafe service identity or execution policy")
	}
	writable := "/var/lib/edgebox/web"
	if name == "edge-agent" {
		writable = "/var/lib/edgebox/agent"
	}
	if values["ReadWritePaths"] != writable {
		return errors.New("unapproved application writable path")
	}
	for _, key := range []string{"PrivateTmp", "PrivateDevices", "ProtectHome", "ProtectKernelTunables", "ProtectKernelModules", "ProtectControlGroups"} {
		if values[key] != "yes" {
			return errors.New("required service isolation missing")
		}
	}
	if values["Type"] != "simple" || values["WantedBy"] != "multi-user.target" || values["UMask"] != "0077" {
		return errors.New("unsafe service lifecycle")
	}
	for _, key := range []string{"After", "Wants"} {
		for _, dependency := range strings.Fields(values[key]) {
			if dependency != "network.target" && dependency != "edge-updater.service" && dependency != "edgebox-ap-isolation.service" {
				return errors.New("unapproved service dependency")
			}
		}
	}
	return nil
}
