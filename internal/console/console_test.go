package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassword = "river lantern"

type fakeBackend struct{}

func (fakeBackend) Snapshot(context.Context) (any, error) {
	return map[string]string{"management": "unknown"}, nil
}
func (fakeBackend) Action(context.Context, string, json.RawMessage) (any, error) {
	return map[string]bool{"ok": true}, nil
}
func testAuth(t *testing.T, clock *time.Time) *Auth {
	t.Helper()
	file := filepath.Join(t.TempDir(), "auth.json")
	if err := SetPassword(file, testPassword); err != nil {
		t.Fatal(err)
	}
	a, err := OpenAuth(file, func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestPasswordPoliciesAndRecordValidation(t *testing.T) {
	for _, password := range []string{"short", "password", "12345678", "admin1234", "bad\npassphrase", "        ", "00000000", "aaaaaaaa", "password ", "bad\tpassphrase"} {
		if ValidatePassword(password) == nil {
			t.Errorf("accepted %q", password)
		}
	}
	for _, password := range []string{"plainwords", "river lantern", "好记的密码足够长了"} {
		if err := ValidatePassword(password); err != nil {
			t.Errorf("composition rules rejected readable password: %v", err)
		}
	}
}
func TestAuthenticationThrottlingExpiryAndInvalidation(t *testing.T) {
	now := time.Now()
	a := testAuth(t, &now)
	for i := 0; i < 5; i++ {
		if _, err := a.Login("admin", "incorrect", "peer"); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if _, err := a.Login("admin", testPassword, "peer"); err == nil || err.Error() != "rate_limited" {
		t.Fatal("not throttled")
	}
	now = now.Add(time.Minute + time.Second)
	first, err := a.Login("admin", testPassword, "peer")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Login("admin", testPassword, "peer")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token || first.CSRF == second.CSRF {
		t.Fatal("session tokens reused")
	}
	if err = a.ChangePassword("wrong current", "new readable words"); err == nil {
		t.Fatal("password changed without current password")
	}
	if err = a.ChangePassword(testPassword, "new readable words"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Session(first.Token); ok {
		t.Fatal("old session survived password change")
	}
	if _, ok := a.Session(second.Token); ok {
		t.Fatal("another session survived password change")
	}
	third, err := a.Login("admin", "new readable words", "peer")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(8 * time.Hour)
	if _, ok := a.Session(third.Token); ok {
		t.Fatal("session did not expire")
	}
}
func TestHTTPAuthenticationCSRFOriginAndCookie(t *testing.T) {
	now := time.Now()
	auth := testAuth(t, &now)
	nodes, err := OpenNodes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := Server{Auth: auth, Nodes: nodes, Backend: fakeBackend{}, Version: "test", Hosts: []string{"localhost"}}
	handler := s.Handler()
	request := func(method, path, body, csrf string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/dashboard", "", "", nil, ""); w.Code != 401 {
		t.Fatal("unauthenticated diagnostics exposed")
	}
	if request("POST", "/api/updates/download", `{"url":"https://example.org/release.zip"}`, "", nil, "").Code != 401 {
		t.Fatal("unauthenticated download accepted")
	}
	w := request("POST", "/api/login", `{"username":"admin","password":"river lantern"}`, "", nil, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	var session Session
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	if request("POST", "/api/updates/download", `{"url":"https://example.org/release.zip"}`, "", cookies[0], "").Code != 403 {
		t.Fatal("download without CSRF accepted")
	}
	validNodes := `{"expected":0,"desired":{"schema":1,"profiles":[],"preferred":"","revision":0,"active":"","operational_status":""}}`
	if request("POST", "/api/nodes", validNodes, "", cookies[0], "").Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if request("POST", "/api/nodes", validNodes, session.CSRF, cookies[0], "http://attacker.invalid").Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	if request("POST", "/api/nodes", validNodes, session.CSRF, cookies[0], "http://localhost").Code != 200 {
		t.Fatal("valid mutation failed")
	}
	if request("POST", "/api/shell", `{"command":"id"}`, session.CSRF, cookies[0], "").Code == 200 {
		t.Fatal("arbitrary command endpoint exists")
	}
	badHost := httptest.NewRequest("GET", "http://attacker.invalid/ready", nil)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, badHost)
	if out.Code != 403 {
		t.Fatal("DNS-rebinding host accepted")
	}
}
func TestNodeValidationImmutabilityAndRollback(t *testing.T) {
	s, err := OpenNodes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	next := Nodes{Schema: 1, Preferred: "one", Profiles: []Node{{ID: "one", Name: "test node", Endpoint: "quic://node.invalid:443", Transport: "generic"}}}
	saved, err := s.Apply(next, 0)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Active != "" || saved.OperationalStatus != "not_implemented" {
		t.Fatal("false active node")
	}
	saved.Profiles[0].Name = "mutated"
	snapshot, _ := s.Snapshot()
	if snapshot.Profiles[0].Name != "test node" {
		t.Fatal("mutable snapshot")
	}
	if _, err = s.Apply(next, 0); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err = s.Rollback(1); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = s.Snapshot()
	if snapshot.Revision != 2 || len(snapshot.Profiles) != 0 {
		t.Fatal("previous state not recovered")
	}
	next.Profiles[0].Endpoint = "https://user:password@node.invalid?token=secret"
	if next.Validate() == nil {
		t.Fatal("credential-bearing endpoint accepted")
	}
}
