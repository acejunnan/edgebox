package network

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

type recorded struct {
	commands []string
	batches  []string
	fail     string
}

func (r *recorded) Run(_ context.Context, in io.Reader, n string, a ...string) ([]byte, error) {
	cmd := n + " " + strings.Join(a, " ")
	r.commands = append(r.commands, cmd)
	if in != nil {
		b, _ := io.ReadAll(in)
		r.batches = append(r.batches, string(b))
	}
	if strings.Contains(cmd, r.fail) && r.fail != "" {
		return nil, fmt.Errorf("injected failure")
	}
	if cmd == "nft list table inet edgebox" {
		return nil, fmt.Errorf("absent")
	}
	return nil, nil
}
func TestRelayFailClosedAndManagementExclusion(t *testing.T) {
	c := EgressConfig{Role: "relay", Interface: "ebtest0", Source: "10.77.0.1", Table: 177, Priority: 10777, BandwidthMbit: 10}
	r := &recorded{}
	if e := c.Apply(context.Background(), r, WAN{}); e != nil {
		t.Fatal(e)
	}
	got := strings.Join(r.commands, "\n")
	for _, s := range []string{"route replace unreachable default table 177 metric 32760", "rule add priority 10777 from 10.77.0.1/32 lookup 177"} {
		if !strings.Contains(got, s) {
			t.Fatalf("missing %s", s)
		}
	}
	if strings.Contains(got, "lookup main") || strings.Contains(got, "0.0.0.0/0") || strings.Contains(got, "nft") {
		t.Fatal(got)
	}
	if e := c.Stop(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	for _, cmd := range r.commands {
		if strings.Contains(cmd, "route flush") && !strings.HasSuffix(cmd, "table 177") {
			t.Fatal("unscoped flush", cmd)
		}
	}
}
func TestEdgeNftFailureDoesNotEnablePolicyRule(t *testing.T) {
	c := EgressConfig{Role: "edge", Interface: "ebtest0", Source: "10.77.0.1", Table: 177, Priority: 10777, BandwidthMbit: 10}
	r := &recorded{fail: "nft -c"}
	if e := c.Apply(context.Background(), r, WAN{Gateway: "192.168.1.1", Dev: "eth0"}); e == nil {
		t.Fatal("expected failure")
	}
	for _, cmd := range r.commands {
		if strings.Contains(cmd, "rule add") {
			t.Fatal("policy enabled after failed nft check")
		}
	}
	if len(r.batches) != 1 || strings.Contains(r.batches[0], "flush ruleset") || !strings.Contains(r.batches[0], "ip daddr @nonpublic counter drop") {
		t.Fatal("unsafe nft batch")
	}
}
func TestWANValidationRejectsInjection(t *testing.T) {
	c := EgressConfig{Role: "edge", Interface: "ebtest0", Source: "10.77.0.1", Table: 177, Priority: 10777, BandwidthMbit: 10}
	r := &recorded{}
	if e := c.Apply(context.Background(), r, WAN{Gateway: "192.168.1.1", Dev: "eth0\nflush ruleset"}); e == nil {
		t.Fatal("accepted invalid interface")
	}
	if len(r.batches) != 0 {
		t.Fatal("issued nft batch for invalid WAN")
	}
}
func TestUnlimitedBandwidthSkipsTC(t *testing.T) {
	c := EgressConfig{Role: "edge", Interface: "ebtest0", Source: "10.77.0.1", Table: 177, Priority: 10777, BandwidthMbit: 0}
	if err := c.Validate(); err != nil {
		t.Fatalf("BandwidthMbit=0 should be valid, got: %v", err)
	}
	r := &recorded{}
	if err := c.Apply(context.Background(), r, WAN{Gateway: "192.168.1.1", Dev: "eth0"}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range r.commands {
		if strings.HasPrefix(cmd, "tc ") {
			t.Fatalf("tc command executed when bandwidth is unlimited: %s", cmd)
		}
	}
}
