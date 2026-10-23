//go:build linux

package hostops

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPrivilegedBoundaryRejectsArbitraryOperations(t *testing.T) {
	b := &Broker{}
	for _, payload := range []string{`{"url":"http://example.org/update.zip"}`, `{"url":"https://127.0.0.1/update.zip"}`, `{"url":"https://192.168.50.1/update.zip"}`, `{"url":"https://example.org/update.zip","command":"id"}`, `{"url":"https://user:password@example.org/update.zip"}`} {
		if _, err := b.action(context.Background(), "update_download", json.RawMessage(payload)); err == nil {
			t.Fatal("unsafe download accepted")
		}
	}
	for _, test := range []struct{ kind, payload string }{{"shell", `{"command":"id"}`}, {"restart", `{"service":"ssh.service"}`}, {"restart", `{"service":"web","command":"id"}`}, {"probes", `{"target":"file:///etc/shadow"}`}, {"update", `{"release":"../../etc/shadow"}`}, {"update", `{"release":"valid","command":"id"}`}, {"hotspot", `{"schema":1,"ssid":"bad\nsection","password":"readableword12"}`}} {
		if _, err := b.action(context.Background(), test.kind, json.RawMessage(test.payload)); err == nil {
			t.Errorf("accepted unsupported operation %s", test.kind)
		}
	}
}
