package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadURLAndDialRestrictions(t *testing.T) {
	for _, raw := range []string{"http://example.com/release.zip", "https://admin:password@example.com/p.zip", "https://example.com/p.zip?token=secret", "https://example.com/p.zip#secret", "https://localhost/x", "https://127.0.0.1/x", "https://192.168.50.1/x", "https://169.254.169.254/x", "https://100.64.1.1/x", "https://[::1]/x", "https://[::ffff:127.0.0.1]/x", "https://example.com:8080/x"} {
		if ValidateDownloadURL(raw) == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
	if err := ValidateDownloadURL("https://downloads.example.org/releases/latest.zip"); err != nil {
		t.Fatal(err)
	}
	c := downloadClient()
	if c.CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("redirect allowed")
	}
	transport := c.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("download must not inherit a proxy bypassing destination checks")
	}
	for _, address := range []string{"127.0.0.1:443", "192.168.50.1:443", "[::1]:443", "169.254.169.254:443", "100.64.1.2:443"} {
		if conn, err := transport.DialContext(context.Background(), "tcp", address); err == nil {
			conn.Close()
			t.Fatal("private resolved destination accepted")
		}
	}
}

func TestDownloadedPackagesMustBeAuthenticBeforeAdmission(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	archive := packageFile(t, t.TempDir(), "downloaded", 4, priv, nil)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: responseTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header)}, nil
	})}
	inbox := t.TempDir()
	m, err := receive(context.Background(), "https://example.org/release.zip", inbox, pub, "linux", "arm64", client)
	if err != nil || m.Version != "downloaded" {
		t.Fatalf("verified download failed: %v", err)
	}
	if _, err = Verify(filepath.Join(inbox, "downloaded.zip"), pub, "linux", "arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err = receive(context.Background(), "https://example.org/release.zip", inbox, pub, "linux", "arm64", client); err == nil {
		t.Fatal("approved version overwritten")
	}
	wrong, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, key := range []ed25519.PublicKey{wrong} {
		badInbox := t.TempDir()
		if _, err = receive(context.Background(), "https://example.org/release.zip", badInbox, key, "linux", "arm64", client); err == nil {
			t.Fatal("untrusted package admitted")
		}
		entries, _ := os.ReadDir(badInbox)
		if len(entries) != 0 {
			t.Fatal("rejected download left artifacts")
		}
	}
	data = []byte("corrupt zip")
	if _, err = receive(context.Background(), "https://example.org/release.zip", t.TempDir(), pub, "linux", "arm64", client); err == nil {
		t.Fatal("corrupt package admitted")
	}
}

func TestInterruptedDownloadInboxRecoveryIsScoped(t *testing.T) {
	inbox := t.TempDir()
	for _, name := range []string{".incoming-123", ".download-456", "approved.zip", ".edgebox-tmp-key"} {
		if err := os.WriteFile(filepath.Join(inbox, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(inbox, ".incoming-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := RecoverDownloads(inbox); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"approved.zip", ".edgebox-tmp-key", ".incoming-directory"} {
		if _, err := os.Stat(filepath.Join(inbox, name)); err != nil {
			t.Fatal("unowned file removed")
		}
	}
	for _, name := range []string{".incoming-123", ".download-456"} {
		if _, err := os.Stat(filepath.Join(inbox, name)); !os.IsNotExist(err) {
			t.Fatal("abandoned download not cleaned")
		}
	}
}
