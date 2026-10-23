package setup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestHostInspectionStopsBeforePasswordAuthentication(t *testing.T) {
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(key)
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	authenticated := make(chan struct{}, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		config := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			authenticated <- struct{}{}
			return nil, errors.New("unexpected authentication")
		}}
		config.AddHostKey(signer)
		_, _, _, _ = ssh.NewServerConn(conn, config)
	}()
	addr := netip.MustParseAddrPort(listener.Addr().String())
	id, e := Inspect(context.Background(), Host{IP: "127.0.0.1", Port: int(addr.Port()), User: "root", Password: "must-not-be-sent"})
	if e != nil {
		t.Fatal(e)
	}
	if id.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("wrong fingerprint")
	}
	<-finished
	select {
	case <-authenticated:
		t.Fatal("inspection attempted authentication")
	default:
	}
}

func testRequest() Request {
	return Request{VPS: Host{IP: "203.0.113.10", User: "root", Port: 22}, Edge: Host{IP: "192.168.1.20", User: "root", Port: 22}}
}

type fakeMachine struct {
	name string
	log  *[]string
	fail string
}

func (f *fakeMachine) Run(_ context.Context, command, input string) (string, error) {
	if command == "bash -s" {
		*f.log = append(*f.log, f.name+":bootstrap")
		return "ready", nil
	}
	var r map[string]any
	if e := json.Unmarshal([]byte(input), &r); e != nil {
		return "", e
	}
	mode, _ := r["mode"].(string)
	*f.log = append(*f.log, f.name+":"+mode)
	if mode == f.fail {
		return "", errors.New("injected failure")
	}
	switch mode {
	case "edge-key":
		return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8g fixture", nil
	case "public-key":
		return strings.Repeat("A", 43) + "=", nil
	case "egress":
		return "198.51.100.20", nil
	case "client-link":
		return "vless://fixture@203.0.113.10:443", nil
	}
	return "ready", nil
}
func (f *fakeMachine) Reverse(context.Context, Host, Identity) error {
	*f.log = append(*f.log, f.name+":fresh-reverse-login")
	return nil
}
func (f *fakeMachine) Close() error { return nil }
func TestInstallOrdersManagementBeforeDataAndVerifiesBeforeCommit(t *testing.T) {
	var log []string
	vps := &fakeMachine{name: "vps", log: &log}
	edge := &fakeMachine{name: "edge", log: &log}
	installer := Installer{SourceRef: strings.Repeat("a", 40), Dial: func(_ context.Context, h Host, _ Identity) (Machine, error) {
		if h.IP == testRequest().VPS.IP {
			return vps, nil
		}
		return edge, nil
	}}
	id := Identity{PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8g"}
	result, e := installer.Install(context.Background(), testRequest(), id, id)
	if e != nil {
		t.Fatal(e)
	}
	if result.PublicIP != "198.51.100.20" {
		t.Fatal(result)
	}
	position := func(s string) int {
		for n, v := range log {
			if v == s {
				return n
			}
		}
		t.Fatalf("missing %s: %v", s, log)
		return -1
	}
	if position("vps:fresh-reverse-login") > position("edge:configure") {
		t.Fatal("data configured before fresh management login")
	}
	if position("vps:egress") > position("edge:commit") {
		t.Fatal("committed before actual proxy egress test")
	}
	if position("vps:healthy") > position("vps:commit") {
		t.Fatal("committed before WAN validation")
	}
}
func TestConfigureFailureRollsBackBothAndPreservesManagement(t *testing.T) {
	var log []string
	vps := &fakeMachine{name: "vps", log: &log}
	edge := &fakeMachine{name: "edge", log: &log, fail: "configure"}
	i := Installer{SourceRef: strings.Repeat("a", 40), Dial: func(_ context.Context, h Host, _ Identity) (Machine, error) {
		if h.IP == testRequest().VPS.IP {
			return vps, nil
		}
		return edge, nil
	}}
	id := Identity{PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8g"}
	if _, e := i.Install(context.Background(), testRequest(), id, id); e == nil {
		t.Fatal("failure ignored")
	}
	joined := strings.Join(log, ",")
	for _, want := range []string{"vps:management-commit", "edge:safe-mode", "vps:safe-mode"} {
		if !strings.Contains(joined, want) {
			t.Fatal(joined)
		}
	}
	if strings.Contains(joined, ":commit") || strings.Contains(joined, ":start") {
		t.Fatal("continued after configuration error")
	}
}
func TestWizardRejectsCrossOriginAndUnconfirmedStart(t *testing.T) {
	s := NewServer("127.0.0.1:12345", "")
	handler := s.Handler()
	for _, test := range []struct{ host, origin, token string }{{"attacker.invalid", "", s.Token}, {s.Host, "http://attacker.invalid", s.Token}, {s.Host, "", "wrong"}} {
		r := httptest.NewRequest("GET", "http://"+s.Host+"/api/state", nil)
		r.Host = test.host
		r.Header.Set("Origin", test.origin)
		r.Header.Set("X-Setup-Token", test.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	body := `{"request":{"vps":{"ip":"203.0.113.10"},"edge":{"ip":"192.168.1.20"}},"confirmed":true}`
	r := httptest.NewRequest("POST", "http://"+s.Host+"/api/start", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Setup-Token", s.Token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatal("started without inspected host keys", w.Code)
	}
}
func TestInspectionKeepsNoCredentialsAndNeverInstalls(t *testing.T) {
	s := NewServer("127.0.0.1:12345", "")
	s.inspect = func(context.Context, Host) (Identity, error) { return Identity{Fingerprint: "SHA256:fixture"}, nil }
	s.install = func(context.Context, Request, Identity, Identity) (Result, error) {
		t.Error("inspection installed software")
		return Result{}, nil
	}
	req := testRequest()
	req.VPS.Password = "private-login-secret"
	req.Edge.Passphrase = "private-key-passphrase"
	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "http://"+s.Host+"/api/inspect", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Setup-Token", s.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.inspected.VPS.Password != "" || s.inspected.Edge.Passphrase != "" {
		t.Fatal("credentials retained by inspection")
	}
	data, _ := json.Marshal(s.state)
	if strings.Contains(string(data), "private-") {
		t.Fatal("secret included in progress")
	}
}
func TestInvalidInputsAndSourceAreRejectedBeforeDial(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "169.254.1.1", "bad;touch /tmp/x", "::1"} {
		r := testRequest()
		r.Edge.IP = ip
		if r.Validate() == nil {
			t.Fatal(ip)
		}
	}
	r := testRequest()
	r.VPS.IP = "192.168.1.1"
	if r.Validate() == nil {
		t.Fatal("private VPS accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e := (Installer{SourceRef: "main;bad"}).Install(ctx, testRequest(), Identity{}, Identity{}); e == nil {
		t.Fatal("unvalidated source accepted")
	}
}
