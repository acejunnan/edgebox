package ap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDHCPDoesNotRemainDependencyFailedAfterDelayedWiFi(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "ap", "edge-ap-dhcp.service"))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(data)
	if !strings.Contains(unit, "Requires=edgebox-ap-isolation.service\n") || !strings.Contains(unit, "Wants=edge-ap.service\n") || !strings.Contains(unit, "Restart=always\n") {
		t.Fatal("DHCP must retain mandatory isolation but retry independently of AP startup")
	}
	if strings.Contains(unit, "Requires=edgebox-ap-isolation.service edge-ap.service") {
		t.Fatal("delayed AP would permanently cancel DHCP job")
	}
}

func TestIsolatedAPProfileAndCredentials(t *testing.T) {
	c := Config{Schema: 1, SSID: "EdgeBox-Test", Password: "readableword12"}
	data, err := c.Profile("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if err != nil {
		t.Fatal(err)
	}
	profile := string(data)
	for _, required := range []string{"mode=ap", "method=manual", "never-default=true", "key-mgmt=wpa-psk", "pmf=1", "proto=rsn", "pairwise=ccmp", "method=disabled"} {
		if !strings.Contains(profile, required) {
			t.Error(required)
		}
	}
	if strings.Contains(profile, "shared") || strings.Contains(profile, "gateway=") {
		t.Fatal("hotspot provides Internet sharing")
	}
	if c.Redacted().Password != "" {
		t.Fatal("credential exposed")
	}
	if strings.Contains(fmt.Sprint(c), c.Password) || strings.Contains(c.LogValue().String(), c.Password) {
		t.Fatal("credential leaked through diagnostic representation")
	}
	for _, value := range []string{"bad\nssid", "evil[section]", "bad\\escape"} {
		c.SSID = value
		if c.Validate() == nil {
			t.Fatal("config injection accepted")
		}
	}
	dhcp := DHCPConfig()
	if !strings.Contains(dhcp, "dhcp-option=3\n") || !strings.Contains(dhcp, "interface=wlan0") {
		t.Fatal("DHCP not WLAN-scoped or advertises default gateway")
	}
	rules := IsolationRules()
	if strings.Contains(rules, "flush ruleset") || !strings.Contains(rules, "iifname \"wlan0\" drop") || !strings.Contains(rules, "oifname \"wlan0\" drop") {
		t.Fatal("unsafe forwarding isolation")
	}
}

func TestEightCharacterWiFiPasswordIsAccepted(t *testing.T) {
	c := Config{Schema: 1, SSID: "EdgeBox-Test", Password: "readable"}
	if _, err := c.Profile("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"); err != nil {
		t.Fatal("valid eight-character WPA2 password rejected:", err)
	}
	c.Password = "76543210"
	if err := c.Validate(); err != nil {
		t.Fatal("numeric password rejected:", err)
	}
	for _, n := range []int{0, 7, 64} {
		c.Password = strings.Repeat("a", n)
		if c.Validate() == nil {
			t.Fatalf("invalid password length %d accepted", n)
		}
	}
}
