package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"
)

type CommandRunner interface {
	Run(context.Context, io.Reader, string, ...string) ([]byte, error)
}

// EgressConfig contains no transport credentials or protocol-specific values.
type EgressConfig struct {
	Role          string `json:"role"`
	Interface     string `json:"interface"`
	Source        string `json:"source"`
	Table         int    `json:"table"`
	Priority      int    `json:"priority"`
	BandwidthMbit int    `json:"bandwidth_mbit"`
}
type WAN struct {
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
}

var safeInterface = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`)

func (c EgressConfig) Validate() error {
	a, e := netip.ParseAddr(c.Source)
	if (c.Role != "edge" && c.Role != "relay") || !safeInterface.MatchString(c.Interface) || !strings.HasPrefix(c.Interface, "eb") || e != nil || !a.Is4() || !a.IsPrivate() || c.Table < 1 || c.Table > 252 || c.Priority < 1 || c.Priority >= 32766 || c.BandwidthMbit < 0 || c.BandwidthMbit > 10000 {
		return fmt.Errorf("invalid egress configuration")
	}
	return nil
}
func DiscoverWAN(ctx context.Context, r CommandRunner) (WAN, error) {
	b, e := r.Run(ctx, nil, "ip", "-j", "-4", "route", "show", "table", "main", "default")
	if e != nil {
		return WAN{}, e
	}
	var routes []WAN
	if e = json.Unmarshal(b, &routes); e != nil {
		return WAN{}, e
	}
	for _, w := range routes {
		a, e := netip.ParseAddr(w.Gateway)
		if e == nil && a.Is4() && safeInterface.MatchString(w.Dev) && !strings.HasPrefix(w.Dev, "eb") {
			return w, nil
		}
	}
	return WAN{}, fmt.Errorf("physical WAN default route unavailable")
}
func (c EgressConfig) ruleArgs(action string) []string {
	selector := []string{"from", c.Source + "/32"}
	if c.Role == "edge" {
		selector = []string{"iif", c.Interface}
	}
	return append(append([]string{"-4", "rule", action, "priority", fmt.Sprint(c.Priority)}, selector...), "lookup", fmt.Sprint(c.Table))
}
func (c EgressConfig) Nft(w WAN) string {
	return fmt.Sprintf(`table inet edgebox {
 set nonpublic { type ipv4_addr; flags interval; elements = { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.0.2.0/24, 192.168.0.0/16, 198.18.0.0/15, 198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/3 } }
 chain input { type filter hook input priority -5; policy accept; iifname "%s" counter drop; }
 chain forward { type filter hook forward priority -5; policy accept;
  iifname "%s" ip daddr @nonpublic counter drop
  iifname "%s" ip saddr %s oifname "%s" ct state { new, established, related } tcp flags syn tcp option maxseg size set rt mtu
  iifname "%s" ip saddr %s oifname "%s" ct state { new, established, related } counter accept
  iifname "%s" counter drop
  oifname "%s" iifname "%s" ip daddr %s ct state { established, related } counter accept
  oifname "%s" counter drop
 }
 chain postrouting { type nat hook postrouting priority 90; policy accept; iifname "%s" ip saddr %s oifname "%s" counter masquerade; }
}
`, c.Interface, c.Interface, c.Interface, c.Source, w.Dev, c.Interface, c.Source, w.Dev, c.Interface, c.Interface, w.Dev, c.Source, c.Interface, c.Interface, c.Source, w.Dev)
}
func (c EgressConfig) Apply(ctx context.Context, r CommandRunner, w WAN) error {
	if e := c.Validate(); e != nil {
		return e
	}
	run := func(name string, args ...string) error { _, e := r.Run(ctx, nil, name, args...); return e }
	table := fmt.Sprint(c.Table)
	// The terminal unreachable route prevents fallthrough to the management table.
	if e := run("ip", "-4", "route", "replace", "unreachable", "default", "table", table, "metric", "32760"); e != nil {
		return e
	}
	if c.Role == "edge" {
		if !safeInterface.MatchString(w.Dev) || w.Dev == c.Interface {
			return fmt.Errorf("invalid WAN")
		}
		if a, e := netip.ParseAddr(w.Gateway); e != nil || !a.Is4() {
			return fmt.Errorf("invalid WAN gateway")
		}
		batch := c.Nft(w)
		if _, e := r.Run(ctx, strings.NewReader(batch), "nft", "-c", "-f", "-"); e != nil {
			return e
		}
		if _, e := r.Run(ctx, nil, "nft", "list", "table", "inet", "edgebox"); e == nil {
			batch = "delete table inet edgebox\n" + batch
		}
		if _, e := r.Run(ctx, strings.NewReader(batch), "nft", "-f", "-"); e != nil {
			return e
		}
		if e := run("ip", "-4", "route", "replace", "default", "via", w.Gateway, "dev", w.Dev, "onlink", "table", table, "metric", "100"); e != nil {
			return e
		}
		if e := run("ip", "-4", "route", "replace", c.Source+"/32", "dev", c.Interface); e != nil {
			return e
		}
		if c.BandwidthMbit > 0 {
			rate := fmt.Sprintf("%dmbit", c.BandwidthMbit)
			if e := run("tc", "qdisc", "replace", "dev", c.Interface, "root", "tbf", "rate", rate, "burst", "128kb", "latency", "400ms"); e != nil {
				return e
			}
			if e := run("tc", "qdisc", "replace", "dev", c.Interface, "handle", "ffff:", "ingress"); e != nil {
				return e
			}
			if e := run("tc", "filter", "replace", "dev", c.Interface, "parent", "ffff:", "protocol", "ip", "pref", "1", "matchall", "action", "police", "rate", rate, "burst", "128kb", "conform-exceed", "drop"); e != nil {
				return e
			}
		}
	} else {
		if e := run("ip", "-4", "route", "replace", "default", "dev", c.Interface, "table", table, "metric", "100"); e != nil {
			return e
		}
	}
	// Caller checks exclusive table/priority ownership before the first apply.
	_, _ = r.Run(ctx, nil, "ip", c.ruleArgs("del")...)
	return run("ip", c.ruleArgs("add")...)
}
func (c EgressConfig) Stop(ctx context.Context, r CommandRunner) error {
	// Exact selectors and an exclusively reserved table; never change main default.
	var failures []error
	if _, e := r.Run(ctx, nil, "ip", c.ruleArgs("del")...); e != nil { /* absent rule is already safe */
	}
	if c.Role == "edge" {
		if _, e := r.Run(ctx, nil, "nft", "list", "table", "inet", "edgebox"); e == nil {
			if _, e = r.Run(ctx, nil, "nft", "delete", "table", "inet", "edgebox"); e != nil {
				failures = append(failures, e)
			}
		}
		_, _ = r.Run(ctx, nil, "ip", "-4", "route", "del", c.Source+"/32", "dev", c.Interface)
	}
	if _, e := r.Run(ctx, nil, "ip", "-4", "route", "flush", "table", fmt.Sprint(c.Table)); e != nil {
		// iproute2 returns ENOENT when a never-used table does not exist.
		// Confirm absence independently instead of treating every error as success.
		b, inspectErr := r.Run(ctx, nil, "ip", "-j", "-4", "route", "show", "table", "all")
		var routes []struct {
			Table any `json:"table"`
		}
		if inspectErr != nil || json.Unmarshal(b, &routes) != nil {
			failures = append(failures, e)
		} else {
			for _, route := range routes {
				if fmt.Sprint(route.Table) == fmt.Sprint(c.Table) {
					failures = append(failures, e)
					break
				}
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("network cleanup failed: %v", failures)
	}
	return nil
}
