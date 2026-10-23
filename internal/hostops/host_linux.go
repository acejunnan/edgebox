//go:build linux

// Package hostops is the privileged, fixed-operation boundary for the local
// updater socket. No command text from HTTP or release metadata is executed.
package hostops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"edgebox/internal/ap"
	"edgebox/internal/health"
	"edgebox/internal/store"
	"edgebox/internal/updater"
	"golang.org/x/sys/unix"
)

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
func FreeBytes(path string) (uint64, error) {
	var s unix.Statfs_t
	err := unix.Statfs(path, &s)
	return s.Bavail * uint64(s.Bsize), err
}

type Runner struct{}

func (Runner) FreeBytes(path string) (uint64, error) { return FreeBytes(path) }
func (Runner) Restart(ctx context.Context) error {
	for _, name := range []string{"edge-web", "edge-agent"} {
		data, err := os.ReadFile(filepath.Join("/opt/edgebox/current/services", name+".service"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err = updater.ValidateServiceAsset(name, data); err != nil {
			return err
		}
		if err = store.WriteAtomic(filepath.Join("/etc/systemd/system", name+".service"), data, 0644); err != nil {
			return err
		}
	}
	dataplaneSrc := filepath.Join("/opt/edgebox/current/bin", "edge-dataplane")
	if _, err := os.Stat(dataplaneSrc); err == nil {
		data, err := os.ReadFile(dataplaneSrc)
		if err == nil {
			_ = store.WriteAtomic("/opt/edgebox-network/edge-dataplane", data, 0755)
		}
	}
	if _, err := command(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := command(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "edge-dataplane.service"); err == nil {
		if _, err = command(ctx, "/usr/bin/systemctl", "restart", "edge-dataplane.service"); err != nil {
			return err
		}
	}
	if _, err := command(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "edge-agent.service"); err == nil {
		if _, err = command(ctx, "/usr/bin/systemctl", "restart", "edge-agent.service"); err != nil {
			return err
		}
	}
	_, err := command(ctx, "/usr/bin/systemctl", "restart", "edge-web.service")
	return err
}
func (Runner) Verify(ctx context.Context, version string) error {
	client := http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:8080/ready", nil)
		response, err := client.Do(req)
		if err == nil {
			var state struct {
				Ready   bool   `json:"ready"`
				Version string `json:"version"`
			}
			_ = json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&state)
			response.Body.Close()
			if response.StatusCode == 200 && state.Ready && state.Version == version {
				c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:22")
				if err == nil {
					_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
					line, err := bufio.NewReader(c).ReadString('\n')
					c.Close()
					if err == nil && strings.HasPrefix(line, "SSH-") {
						return nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type Broker struct {
	Engine     *updater.Engine
	Inbox      string
	mu         sync.Mutex
	busy       bool
	lastUpdate string
	lastProbe  map[string]any
}

func strictDecode(data json.RawMessage, v any) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
func (b *Broker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/action" {
			http.NotFound(w, r)
			return
		}
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		var request struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload,omitempty"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		result, err := b.action(ctx, request.Kind, request.Payload)
		if err != nil {
			http.Error(w, "operation rejected or failed", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
func (b *Broker) action(ctx context.Context, kind string, payload json.RawMessage) (any, error) {
	switch kind {
	case "update_download":
		var request struct {
			URL string `json:"url"`
		}
		if strictDecode(payload, &request) != nil || updater.ValidateDownloadURL(request.URL) != nil {
			return nil, errors.New("invalid package URL")
		}
		free, err := FreeBytes(b.Inbox)
		if err != nil || free < uint64(updater.MaxPackage)+32*1024*1024 {
			return nil, errors.New("insufficient download space")
		}
		b.mu.Lock()
		if b.busy {
			b.mu.Unlock()
			return nil, errors.New("update busy")
		}
		b.busy = true
		b.lastUpdate = "downloading"
		b.mu.Unlock()
		go func() {
			job, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			_, err := updater.Fetch(job, request.URL, b.Inbox, b.Engine.Key, b.Engine.OS, b.Engine.Arch)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.busy = false
			if err != nil {
				b.lastUpdate = "download_rejected"
			} else {
				b.lastUpdate = "download_verified"
			}
		}()
		return map[string]bool{"accepted": true}, nil
	case "diagnostics":
		return b.snapshot(ctx), nil
	case "probes":
		var empty struct{}
		if strictDecode(payload, &empty) != nil {
			return nil, errors.New("invalid probe request")
		}
		return b.probes(ctx), nil
	case "hotspot":
		var c ap.Config
		if strictDecode(payload, &c) != nil {
			return nil, errors.New("invalid AP settings")
		}
		c.Schema = 1
		return b.changeAP(ctx, c)
	case "update_import":
		var req struct {
			Source string `json:"source"`
		}
		if strictDecode(payload, &req) != nil || !strings.HasPrefix(req.Source, "/var/lib/edgebox/web/") || !strings.HasSuffix(req.Source, ".zip") {
			return nil, errors.New("invalid import source")
		}
		defer os.Remove(req.Source)
		p, err := updater.Verify(req.Source, b.Engine.Key, b.Engine.OS, b.Engine.Arch)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(req.Source)
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(b.Inbox, p.Manifest.Version+".zip")
		if err := store.WriteAtomic(dest, data, 0644); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true, "version": p.Manifest.Version, "manifest": p.Manifest}, nil
	case "restart":
		var request struct {
			Service string `json:"service"`
		}
		if strictDecode(payload, &request) != nil || request.Service != "web" {
			return nil, errors.New("invalid service")
		}
		_, err := command(ctx, "/usr/bin/systemd-run", "--unit=edgebox-web-requested-restart", "--on-active=2s", "/usr/bin/systemctl", "restart", "edge-web.service")
		return map[string]bool{"scheduled": err == nil}, err
	case "update_status":
		b.mu.Lock()
		busy, last := b.busy, b.lastUpdate
		b.mu.Unlock()
		entries, _ := os.ReadDir(b.Inbox)
		available := []updater.Manifest{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") {
				continue
			}
			p, err := updater.Verify(filepath.Join(b.Inbox, entry.Name()), b.Engine.Key, b.Engine.OS, b.Engine.Arch)
			if err == nil {
				available = append(available, p.Manifest)
			}
		}
		return map[string]any{"state": b.Engine.Status(), "busy": busy, "result": last, "available": available, "rollback_available": b.Engine.Status().Previous != ""}, nil
	case "update":
		var request struct {
			Release string `json:"release"`
		}
		if strictDecode(payload, &request) != nil || !updater.VersionPattern.MatchString(request.Release) {
			return nil, errors.New("invalid release")
		}
		archive := filepath.Join(b.Inbox, request.Release+".zip")
		if _, err := updater.Verify(archive, b.Engine.Key, b.Engine.OS, b.Engine.Arch); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.busy {
			b.mu.Unlock()
			return nil, errors.New("update busy")
		}
		b.busy = true
		b.lastUpdate = "running"
		b.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			err := b.Engine.Apply(ctx, archive)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.busy = false
			if err != nil {
				b.lastUpdate = "update_failed_or_rolled_back"
			} else {
				b.lastUpdate = "committed"
			}
		}()
		return map[string]bool{"accepted": true}, nil
	default:
		return nil, errors.New("unknown operation")
	}
}
func readText(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
func managementEndpoint() (string, string) {
	host, port := "", "22"
	data, _ := os.ReadFile("/etc/edgebox/edge-management.ssh_config")
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			if f[0] == "HostName" {
				host = f[1]
			}
			if f[0] == "Port" {
				port = f[1]
			}
		}
	}
	return host, port
}
func serviceState(ctx context.Context) (string, string) {
	data, _ := command(ctx, "/usr/bin/systemctl", "show", "edge-management.service", "--property=MainPID", "--property=ActiveState")
	pid, active := "0", "unknown"
	for _, line := range strings.Split(string(data), "\n") {
		k, v, _ := strings.Cut(line, "=")
		if k == "MainPID" {
			pid = v
		}
		if k == "ActiveState" {
			active = v
		}
	}
	return pid, active
}
func managementObservation(ctx context.Context) health.Observation {
	pid, active := serviceState(ctx)
	host, port := managementEndpoint()
	sockets, _ := command(ctx, "/usr/bin/ss", "-Htnp")
	established := false
	for _, line := range strings.Split(string(sockets), "\n") {
		if strings.HasPrefix(line, "ESTAB") && strings.Contains(line, "pid="+pid+",") && strings.Contains(line, net.JoinHostPort(host, port)) {
			established = true
		}
	}
	status, reason := health.StatusDegraded, "reverse_connection_not_verified"
	if active == "active" && established {
		status = health.StatusHealthy
		reason = "ssh_connection_established"
	}
	return health.Observation{Domain: health.DomainManagement, Status: status, ObservedAt: time.Now().UTC(), ReasonCode: reason, Details: map[string]string{"service": active, "verification": "matching outbound SSH socket; fresh operator login is a separate acceptance test"}}
}
func (b *Broker) snapshot(ctx context.Context) map[string]any {
	interfaces, _ := command(ctx, "/usr/sbin/ip", "-j", "address")
	routes, _ := command(ctx, "/usr/sbin/ip", "-j", "route")
	var ifaceJSON, routeJSON any
	_ = json.Unmarshal(interfaces, &ifaceJSON)
	_ = json.Unmarshal(routes, &routeJSON)
	wifi, _ := command(ctx, "/usr/bin/nmcli", "-g", "GENERAL.STATE,GENERAL.CONNECTION", "device", "show", "wlan0")
	ssid := readAPMetadata()
	ethernet := readText("/sys/class/net/eth0/operstate")
	mem := readText("/proc/meminfo")
	total, available := "", ""
	for _, line := range strings.Split(mem, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			total = strings.TrimSpace(strings.TrimPrefix(line, "MemTotal:"))
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			available = strings.TrimSpace(strings.TrimPrefix(line, "MemAvailable:"))
		}
	}
	free, _ := FreeBytes("/")
	var disk *usage
	var fs unix.Statfs_t
	if unix.Statfs("/", &fs) == nil && fs.Blocks > 0 && fs.Bfree <= fs.Blocks {
		disk = &usage{fs.Blocks * uint64(fs.Bsize), (fs.Blocks - fs.Bfree) * uint64(fs.Bsize), fs.Bavail * uint64(fs.Bsize), float64(fs.Blocks-fs.Bfree) * 100 / float64(fs.Blocks)}
	}
	temperature := readText("/sys/class/thermal/thermal_zone0/temp")
	if t, err := strconv.ParseFloat(temperature, 64); err == nil {
		temperature = fmt.Sprintf("%.1f °C", t/1000)
	}
	logs, _ := command(ctx, "/usr/bin/journalctl", "-u", "edge-management.service", "-b", "--no-pager", "-n", "12", "-o", "cat")
	redacted := []string{}
	host, _ := managementEndpoint()
	for _, line := range strings.Split(string(logs), "\n") {
		lower := strings.ToLower(line)
		sensitive := false
		for _, word := range []string{"password", "token", "secret", "private", "credential", "begin "} {
			if strings.Contains(lower, word) {
				sensitive = true
			}
		}
		if sensitive {
			line = "[sensitive log entry redacted]"
		}
		if host != "" {
			line = strings.ReplaceAll(line, host, "[bastion]")
		}
		if len(line) > 512 {
			line = line[:512]
		}
		if line != "" {
			redacted = append(redacted, line)
		}
	}
	management := managementObservation(ctx)
	report := health.NewReport(time.Now())
	_ = report.Set(management)

	var dataplaneStatus map[string]any
	if dpData, err := os.ReadFile("/var/lib/edgebox/dataplane/status.json"); err == nil {
		_ = json.Unmarshal(dpData, &dataplaneStatus)
	}

	_, bridgeActiveErr := command(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "edge-bridge.service")
	_, mgmtAltActiveErr := command(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "edge-mgmt-alt.service")

	overall := "degraded"
	dpState, _ := dataplaneStatus["state"].(string)
	if ethernet != "up" {
		overall = "wan_disconnected"
	} else if dpState == "ONLINE" {
		overall = "online"
	} else if bridgeActiveErr == nil {
		overall = "fallback_available"
	} else if management.Status == health.StatusHealthy {
		overall = "management_available"
	}

	b.mu.Lock()
	probes := b.lastProbe
	b.mu.Unlock()
	publicIP := ""
	if probes != nil {
		publicIP, _ = probes["public_ip"].(string)
	}
	return map[string]any{
		"device_id":      readText("/var/lib/edgebox/identity/device-id"),
		"version":        b.Engine.Status().Current,
		"vpn_core":       map[string]any{"installed": true, "running": dpState == "ONLINE", "state": dpState, "transport": "wireguard"},
		"dataplane":      dataplaneStatus,
		"fallbacks":      map[string]bool{"edge_bridge": bridgeActiveErr == nil, "edge_mgmt_alt": mgmtAltActiveErr == nil},
		"hardware":       strings.TrimRight(readText("/proc/device-tree/model"), "\x00"),
		"os":             readText("/etc/os-release"),
		"uptime":         readText("/proc/uptime"),
		"uptime_seconds": uptimeSeconds(readText("/proc/uptime")),
		"boot":           readText("/proc/sys/kernel/random/boot_id"),
		"load":           readText("/proc/loadavg"),
		"temperature":    temperature,
		"memory":         total + " / available " + available,
		"memory_usage":   memoryUsage(mem),
		"disk_usage":     disk,
		"storage":        fmt.Sprintf("%.2f GiB free", float64(free)/(1<<30)),
		"ethernet":       ethernet,
		"hotspot":        map[string]any{"ssid": ssid.SSID, "revision": ssid.Revision, "state": strings.TrimSpace(string(wifi)), "address": ap.Address},
		"interfaces":     ifaceJSON,
		"default_route":  routeJSON,
		"public_ip":      publicIP,
		"management":     map[string]any{"status": management.Status, "reason": management.ReasonCode},
		"health":         report.Snapshot(),
		"transport":      "wireguard+tcp_fallback",
		"active_node":    nil,
		"overall":        overall,
		"last_error":     strings.Join(redacted, "\n"),
		"probes":         probes,
	}
}
func (b *Broker) probes(ctx context.Context) map[string]any {
	result := map[string]any{}
	host, port := managementEndpoint()
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	result["bastion_tcp"] = err == nil
	if conn != nil {
		conn.Close()
	}
	dnsCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	_, err = net.DefaultResolver.LookupHost(dnsCtx, "deb.debian.org")
	result["dns"] = err == nil
	client := http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", "https://deb.debian.org", nil)
	response, err := client.Do(req)
	result["outbound_https"] = err == nil && response.StatusCode < 400
	if response != nil {
		response.Body.Close()
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
	response, err = client.Do(req)
	if err == nil {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64))
		response.Body.Close()
		if ip, e := netip.ParseAddr(strings.TrimSpace(string(data))); e == nil {
			result["public_ip"] = ip.String()
		}
	}
	b.mu.Lock()
	b.lastProbe = result
	b.mu.Unlock()
	return result
}

var apUUIDPattern = regexp.MustCompile(`(?m)^uuid=([a-f0-9-]{36})$`)

func readAPMetadata() ap.Config {
	var c ap.Config
	_ = json.Unmarshal([]byte(readText("/var/lib/edgebox/ap/config.json")), &c)
	c.Password = ""
	return c
}
func (b *Broker) changeAP(ctx context.Context, next ap.Config) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := next.Validate(); err != nil {
		return nil, err
	}
	old := readAPMetadata()
	if old.Schema != 1 || old.Revision == ^uint64(0) {
		return nil, errors.New("invalid AP baseline")
	}
	if next.Expected != old.Revision {
		return nil, errors.New("AP revision conflict")
	}
	const file = "/etc/NetworkManager/system-connections/edgebox-management-ap.nmconnection"
	previous, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	matches := apUUIDPattern.FindSubmatch(previous)
	if len(matches) != 2 {
		return nil, errors.New("missing owned connection UUID")
	}
	data, err := next.Profile(string(matches[1]))
	if err != nil {
		return nil, err
	}
	if err = store.WriteAtomic("/var/lib/edgebox/ap/profile.previous", previous, 0600); err != nil {
		return nil, err
	}
	journal, _ := json.Marshal(struct {
		Phase    string    `json:"phase"`
		Previous ap.Config `json:"previous"`
	}{"prepared", old})
	if err = store.WriteAtomic("/var/lib/edgebox/ap/pending", journal, 0600); err != nil {
		return nil, err
	}
	rollback := func(cause error) error {
		// Keep the durable prepare journal until restoration really succeeds.
		// A cancelled HTTP request must not cancel recovery or hang it forever.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		return errors.Join(cause, RecoverAP(recoveryCtx))
	}
	if err = store.WriteAtomic(file, data, 0600); err != nil {
		return nil, rollback(err)
	}
	if _, err = command(ctx, "/usr/bin/nmcli", "connection", "reload"); err != nil {
		return nil, rollback(err)
	}
	if _, err = command(ctx, "/usr/bin/nmcli", "connection", "up", ap.Profile); err != nil {
		return nil, rollback(err)
	}
	next.Revision = old.Revision + 1
	next.Password = ""
	next.Expected = 0
	data, _ = json.Marshal(next)
	if err = store.WriteAtomic("/var/lib/edgebox/ap/config.json", data, 0600); err != nil {
		return nil, rollback(err)
	}
	journal, _ = json.Marshal(struct {
		Phase    string    `json:"phase"`
		Previous ap.Config `json:"previous"`
	}{"committed", old})
	if err = store.WriteAtomic("/var/lib/edgebox/ap/pending", journal, 0600); err != nil {
		return nil, rollback(err)
	}
	if err = os.Remove("/var/lib/edgebox/ap/pending"); err != nil {
		return nil, err
	}
	return next, nil
}
func RecoverAP(ctx context.Context) error {
	if _, err := os.Stat("/var/lib/edgebox/ap/pending"); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	journal, err := os.ReadFile("/var/lib/edgebox/ap/pending")
	if err != nil {
		return err
	}
	var pending struct {
		Phase    string    `json:"phase"`
		Previous ap.Config `json:"previous"`
	}
	if err = json.Unmarshal(journal, &pending); err != nil {
		return err
	}
	if pending.Phase == "committed" {
		return os.Remove("/var/lib/edgebox/ap/pending")
	}
	if pending.Phase != "prepared" || pending.Previous.Schema != 1 {
		return errors.New("invalid AP recovery journal")
	}
	data, err := os.ReadFile("/var/lib/edgebox/ap/profile.previous")
	if err != nil {
		return err
	}
	if err = store.WriteAtomic("/etc/NetworkManager/system-connections/edgebox-management-ap.nmconnection", data, 0600); err != nil {
		return err
	}
	if _, err = command(ctx, "/usr/bin/nmcli", "connection", "reload"); err != nil {
		return err
	}
	if _, err = command(ctx, "/usr/bin/nmcli", "connection", "up", ap.Profile); err != nil {
		return err
	}
	metadata, _ := json.Marshal(pending.Previous.Redacted())
	if err = store.WriteAtomic("/var/lib/edgebox/ap/config.json", metadata, 0600); err != nil {
		return err
	}
	return os.Remove("/var/lib/edgebox/ap/pending")
}
