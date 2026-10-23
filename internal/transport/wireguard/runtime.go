// Package wireguard owns only the WireGuard packet endpoint lifecycle.
package wireguard

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"regexp"
)

type Runner interface {
	Run(context.Context, io.Reader, string, ...string) ([]byte, error)
}
type Config struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
	MTU       int    `json:"mtu"`
	File      string `json:"file"`
}

var interfaceName = regexp.MustCompile(`^eb[a-z0-9]{1,12}$`)

func (c Config) Validate() error {
	p, e := netip.ParsePrefix(c.Address)
	if !interfaceName.MatchString(c.Interface) || e != nil || !p.Addr().Is4() || p.Bits() != 32 || c.MTU < 1280 || c.MTU > 1500 || c.File != "/etc/edgebox/wireguard-v1.conf" {
		return fmt.Errorf("invalid WireGuard endpoint configuration")
	}
	return nil
}
func (c Config) Start(ctx context.Context, r Runner) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if _, e := r.Run(ctx, nil, "ip", "link", "add", "dev", c.Interface, "type", "wireguard"); e != nil {
		return e
	}
	for _, cmd := range [][]string{{"wg", "setconf", c.Interface, c.File}, {"ip", "address", "add", c.Address, "dev", c.Interface}, {"ip", "link", "set", "dev", c.Interface, "mtu", fmt.Sprint(c.MTU), "up"}} {
		if _, e := r.Run(ctx, nil, cmd[0], cmd[1:]...); e != nil {
			_ = c.Stop(ctx, r)
			return e
		}
	}
	return nil
}
func (c Config) Stop(ctx context.Context, r Runner) error {
	_, e := r.Run(ctx, nil, "ip", "link", "del", "dev", c.Interface)
	return e
}
