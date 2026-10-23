// Package ap owns only the isolated management WLAN profile. It never owns
// Ethernet, the host default route, or unrelated NetworkManager connections.
package ap

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"
)

const Profile = "edgebox-management-ap"
const Address = "192.168.50.1"

type Config struct {
	Schema   int    `json:"schema"`
	Revision uint64 `json:"revision"`
	SSID     string `json:"ssid"`
	Password string `json:"password,omitempty"`
	Expected uint64 `json:"expected,omitempty"`
}

func (c Config) String() string {
	return fmt.Sprintf("AP{schema=%d revision=%d ssid=%q password_configured=%t}", c.Schema, c.Revision, c.SSID, c.Password != "")
}
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("schema", c.Schema), slog.Uint64("revision", c.Revision), slog.String("ssid", c.SSID), slog.Bool("password_configured", c.Password != ""))
}

func (c Config) Validate() error {
	if c.Schema != 1 || c.SSID == "" || len(c.SSID) > 32 || !utf8.ValidString(c.SSID) || strings.TrimSpace(c.SSID) != c.SSID || strings.ContainsAny(c.SSID, "\n\r\x00[]=\\;") {
		return errors.New("invalid hotspot SSID")
	}
	if len(c.Password) < 8 || len(c.Password) > 63 {
		return errors.New("Wi-Fi password must contain 8–63 readable ASCII characters")
	}
	for _, r := range c.Password {
		if r < 33 || r > 126 || strings.ContainsRune("[]=\\;", r) {
			return errors.New("invalid Wi-Fi password character")
		}
	}
	return nil
}
func (c Config) Redacted() Config { c.Password = ""; return c }
func (c Config) Profile(uuid string) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if len(uuid) != 36 || strings.ContainsAny(uuid, "\r\n[]=\\;") {
		return nil, errors.New("invalid connection UUID")
	}
	return []byte(fmt.Sprintf(`[connection]
id=%s
uuid=%s
type=wifi
interface-name=wlan0
autoconnect=false
autoconnect-priority=100

[wifi]
mode=ap
ssid=%s
band=bg
channel=6

[wifi-security]
key-mgmt=wpa-psk
pmf=1
proto=rsn;
pairwise=ccmp;
group=ccmp;
psk=%s

[ipv4]
method=manual
address1=192.168.50.1/24
never-default=true
ignore-auto-dns=true

[ipv6]
method=disabled
`, Profile, uuid, c.SSID, c.Password)), nil
}

func DHCPConfig() string {
	return `port=0
interface=wlan0
bind-dynamic
dhcp-range=192.168.50.10,192.168.50.100,255.255.255.0,12h
dhcp-authoritative
dhcp-option=3
dhcp-option=6
dhcp-leasefile=/var/lib/edgebox/ap/leases
pid-file=/run/edgebox-ap-dhcp/dnsmasq.pid
no-hosts
no-resolv
`
}
func IsolationRules() string {
	return `table inet edgebox_management {
 chain isolate_forward {
  type filter hook forward priority -10; policy accept;
  iifname "wlan0" drop
  oifname "wlan0" drop
 }
 chain restrict_web_ingress {
  type filter hook input priority -10; policy accept;
  iifname != "wlan0" iifname != "lo" ip daddr 192.168.50.1 tcp dport 8080 drop
 }
}
`
}
