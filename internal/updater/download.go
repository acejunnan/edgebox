package updater

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ValidateDownloadURL rejects embedded credentials and non-public URL forms.
// DNS answers are checked again at the actual dial; validation alone is not
// relied upon for protection against DNS rebinding or redirects.
func ValidateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") || (u.Port() != "" && u.Port() != "443") {
		return errors.New("public HTTPS package URL required")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicAddress(ip) {
		return errors.New("non-public update address")
	}
	if strings.EqualFold(u.Hostname(), "localhost") || strings.HasSuffix(strings.ToLower(u.Hostname()), ".localhost") {
		return errors.New("local update address")
	}
	return nil
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}

func downloadClient() *http.Client {
	transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true, MaxResponseHeaderBytes: 32 * 1024}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("invalid download destination")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("download name lookup failed")
		}
		for _, ip := range ips {
			if !publicAddress(ip) {
				return nil, errors.New("non-public DNS answer")
			}
		}
		for _, ip := range ips {
			c, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
		}
		return nil, errors.New("download connection failed")
	}
	return &http.Client{Transport: transport, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("update redirects are not allowed") }}
}

// Fetch stages only authenticated packages into the approved inbox. It never
// installs software, changes a release pointer, persists URLs, or logs tokens.
func Fetch(ctx context.Context, raw, inbox string, key ed25519.PublicKey, goos, arch string) (Manifest, error) {
	if err := ValidateDownloadURL(raw); err != nil {
		return Manifest{}, err
	}
	return receive(ctx, raw, inbox, key, goos, arch, downloadClient())
}

func receive(ctx context.Context, raw, inbox string, key ed25519.PublicKey, goos, arch string, client *http.Client) (Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return Manifest{}, errors.New("invalid download request")
	}
	response, err := client.Do(req)
	if err != nil {
		return Manifest{}, errors.New("update download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > MaxPackage {
		return Manifest{}, errors.New("package response rejected")
	}
	f, err := os.CreateTemp(inbox, ".incoming-")
	if err != nil {
		return Manifest{}, err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Close(); err != nil {
		return Manifest{}, err
	}
	if err = SaveDownload(name, response.Body); err != nil {
		return Manifest{}, errors.New("package transfer incomplete or too large")
	}
	p, err := Verify(name, key, goos, arch)
	if err != nil {
		return Manifest{}, errors.New("downloaded package rejected by authenticity or compatibility checks")
	}
	// Link is an atomic no-overwrite admission, unlike an overwriting rename.
	if err = os.Link(name, filepath.Join(inbox, p.Manifest.Version+".zip")); err != nil {
		return Manifest{}, errors.New("package admission failed or version already exists")
	}
	if err = syncDir(inbox); err != nil {
		return Manifest{}, err
	}
	return p.Manifest, nil
}

// RecoverDownloads removes only this downloader's temporary regular files in
// its explicitly owned inbox. It never recurses into other EdgeBox state.
func RecoverDownloads(inbox string) error {
	entries, err := os.ReadDir(inbox)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || (!strings.HasPrefix(entry.Name(), ".incoming-") && !strings.HasPrefix(entry.Name(), ".download-")) {
			continue
		}
		if err = os.Remove(filepath.Join(inbox, entry.Name())); err != nil {
			return err
		}
	}
	return syncDir(inbox)
}
