// edge-dataplane is the independently supervised privileged network executor.
// Signed unprivileged Web application updates cannot replace this executable.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"edgebox/internal/network"
	"edgebox/internal/transport/wireguard"
)

type config struct {
	Transport wireguard.Config     `json:"transport"`
	Network   network.EgressConfig `json:"network"`
}
type runner struct{}

func (runner) Run(ctx context.Context, in io.Reader, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Stdin = in
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	if e != nil {
		return b, fmt.Errorf("%s failed: %w: %s", name, e, strings.TrimSpace(stderr.String()))
	}
	return b, nil
}
func load(path string) (config, error) {
	var c config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if e = c.Transport.Validate(); e != nil {
		return c, e
	}
	if e = c.Network.Validate(); e != nil {
		return c, e
	}
	if c.Transport.Interface != c.Network.Interface {
		return c, errors.New("endpoint mismatch")
	}
	return c, nil
}
func main() {
	if e := run(); e != nil {
		slog.Error("data_plane_stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("config", "/etc/edgebox/dataplane.json", "root-owned validated configuration")
	check := flag.Bool("check", false, "validate without network operations")
	cleanup := flag.Bool("cleanup", false, "remove only reserved data-plane objects")
	flag.Parse()
	c, e := load(*file)
	if e != nil {
		return e
	}
	if *check {
		return nil
	}
	r := runner{}
	stop := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ne := c.Network.Stop(ctx, r)
		_, te := r.Run(ctx, nil, "ip", "link", "show", "dev", c.Transport.Interface)
		if te == nil {
			te = c.Transport.Stop(ctx, r)
		} else {
			te = nil
		}
		return errors.Join(ne, te)
	}
	if *cleanup {
		return stop()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer stop()
	statusPath := "/var/lib/edgebox/dataplane/status.json"
	if e = os.MkdirAll(filepath.Dir(statusPath), 0755); e != nil {
		return e
	}
	report := func(state, reason string, handshake int64) {
		data, _ := json.Marshal(map[string]any{"state": state, "reason": reason, "observed_at": time.Now().UTC(), "last_handshake": handshake, "transport": "wireguard", "interface": c.Transport.Interface, "mtu": c.Transport.MTU, "bandwidth_mbit": c.Network.BandwidthMbit, "internet_egress": "requires_external_verification"})
		tmp := statusPath + ".tmp"
		if e := os.WriteFile(tmp, data, 0644); e == nil {
			_ = os.Rename(tmp, statusPath)
		}
		slog.Info("data_plane_state", "state", state, "reason", reason)
	}
	report("BOOT", "startup_reconciliation", 0)
	// All these identifiers are reserved in the deployment record; crash remnants
	// are removed before constructing a new endpoint. Management is never touched.
	if e := stop(); e != nil {
		return e
	}
	for ctx.Err() == nil {
		w := network.WAN{}
		if c.Network.Role == "edge" {
			w, e = network.DiscoverWAN(ctx, r)
			if e != nil {
				report("NETWORK_WAIT", "physical_wan_unavailable", 0)
				if !wait(ctx, 10*time.Second) {
					break
				}
				continue
			}
		}
		report("CONFIGURING", "applying_owned_endpoint_and_network", 0)
		if e = c.Transport.Start(ctx, r); e == nil {
			e = c.Network.Apply(ctx, r, w)
		}
		if e != nil {
			report("SAFE_MODE", "apply_failed_rolling_back", 0)
			slog.Error("apply_failed", "error", e)
			if ce := stop(); ce != nil {
				return ce
			}
			if !wait(ctx, 30*time.Second) {
				break
			}
			continue
		}
		report("CONNECTING", "waiting_for_authenticated_transport", 0)
		started := time.Now()
		lastState := "CONNECTING"
		for ctx.Err() == nil {
			if !wait(ctx, 10*time.Second) {
				break
			}
			b, err := r.Run(ctx, nil, "wg", "show", c.Transport.Interface, "latest-handshakes")
			var latest int64
			if err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					f := strings.Fields(line)
					if len(f) == 2 {
						v, _ := strconv.ParseInt(f[1], 10, 64)
						if v > latest {
							latest = v
						}
					}
				}
			}
			state := "CONNECTING"
			if latest > 0 && time.Since(time.Unix(latest, 0)) < 180*time.Second {
				state = "ONLINE"
			}
			if state != lastState || latest > 0 {
				report(state, "transport_and_network_observation", latest)
				lastState = state
			}
			if (latest == 0 && time.Since(started) > 180*time.Second) || (latest > 0 && time.Since(time.Unix(latest, 0)) > 180*time.Second) {
				report("SAFE_MODE", "transport_stale", latest)
				break
			}
			if c.Network.Role == "edge" {
				current, we := network.DiscoverWAN(ctx, r)
				if we != nil || current != w {
					report("RECOVERING", "physical_wan_changed", latest)
					break
				}
			}
		}
		if e := stop(); e != nil {
			return e
		}
		if !wait(ctx, 15*time.Second) {
			break
		}
	}
	report("SAFE_MODE", "service_stopped_data_plane_removed", 0)
	return nil
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
