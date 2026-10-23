package wireguard

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

type fake struct{ commands []string }

func (f *fake) Run(_ context.Context, _ io.Reader, n string, a ...string) ([]byte, error) {
	s := n + " " + strings.Join(a, " ")
	f.commands = append(f.commands, s)
	if strings.HasPrefix(s, "wg setconf") {
		return nil, fmt.Errorf("invalid peer")
	}
	return nil, nil
}
func TestFailedConfigurationRemovesEndpointWithoutNetworkPolicy(t *testing.T) {
	c := Config{Interface: "ebtest0", Address: "10.77.0.2/32", MTU: 1380, File: "/etc/edgebox/wireguard-v1.conf"}
	r := &fake{}
	if e := c.Start(context.Background(), r); e == nil {
		t.Fatal("expected failure")
	}
	if r.commands[len(r.commands)-1] != "ip link del dev ebtest0" {
		t.Fatal(r.commands)
	}
	for _, s := range r.commands {
		if strings.Contains(s, "route") || strings.Contains(s, "nft") || strings.Contains(s, "rule") {
			t.Fatal("transport owns network policy", s)
		}
	}
}
