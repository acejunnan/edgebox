package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edgebox/internal/store"
)

func packageFile(t *testing.T, dir, version string, sequence uint64, key ed25519.PrivateKey, mutate func(*Manifest, map[string][]byte)) string {
	t.Helper()
	payload := []byte("approved executable " + version)
	sum := sha256.Sum256(payload)
	m := Manifest{Schema: 1, ConfigSchema: 1, Version: version, Sequence: sequence, OS: "linux", Arch: "arm64", Files: []File{{"bin/edge-web", hex.EncodeToString(sum[:]), int64(len(payload))}}}
	contents := map[string][]byte{"bin/edge-web": payload}
	unit, err := os.ReadFile(filepath.Join("..", "..", "deploy", "console", "edge-web.service"))
	if err != nil {
		t.Fatal(err)
	}
	unitHash := sha256.Sum256(unit)
	m.Files = append(m.Files, File{"services/edge-web.service", hex.EncodeToString(unitHash[:]), int64(len(unit))})
	contents["services/edge-web.service"] = unit
	if mutate != nil {
		mutate(&m, contents)
	}
	data, _ := json.Marshal(m)
	contents["manifest.json"] = data
	contents["manifest.sig"] = ed25519.Sign(key, data)
	file := filepath.Join(dir, version+".zip")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, data := range contents {
		header := zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0644)
		w, err := z.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

type fakeRunner struct {
	failVersion string
	restarts    int
	space       uint64
}

func (r *fakeRunner) Restart(context.Context) error { r.restarts++; return nil }
func (r *fakeRunner) Verify(ctx context.Context, version string) error {
	if version == r.failVersion {
		return errors.New("readiness failed")
	}
	return nil
}
func (r *fakeRunner) FreeBytes(string) (uint64, error) { return r.space, nil }
func TestPackageAuthenticityIntegrityCompatibilityAndPaths(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	good := packageFile(t, dir, "good", 1, private, nil)
	if _, err := Verify(good, public, "linux", "arm64"); err != nil {
		t.Fatal(err)
	}
	wrong, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Verify(good, wrong, "linux", "arm64"); err == nil {
		t.Fatal("untrusted signature accepted")
	}
	if _, err := Verify(good, public, "linux", "amd64"); err == nil {
		t.Fatal("incompatible architecture accepted")
	}
	corrupt := packageFile(t, dir, "corrupt", 2, private, func(m *Manifest, c map[string][]byte) { c["bin/edge-web"] = []byte("corrupt") })
	if _, err := Verify(corrupt, public, "linux", "arm64"); err == nil {
		t.Fatal("corrupted payload accepted")
	}
	traversal := packageFile(t, dir, "traversal", 3, private, func(m *Manifest, c map[string][]byte) { c["../../escape"] = []byte("bad") })
	if _, err := Verify(traversal, public, "linux", "arm64"); err == nil {
		t.Fatal("unexpected path accepted")
	}
	invalid := packageFile(t, dir, "schema", 4, private, func(m *Manifest, c map[string][]byte) { m.ConfigSchema = 2 })
	if _, err := Verify(invalid, public, "linux", "arm64"); err == nil {
		t.Fatal("incompatible schema accepted")
	}
}
func TestCommitFailedReleaseRollbackAndReplay(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	packages := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{space: 1 << 30}
	e, err := Open(root, public, "linux", "arm64", runner)
	if err != nil {
		t.Fatal(err)
	}
	base := packageFile(t, packages, "base", 1, private, nil)
	if err = e.Bootstrap(base); err != nil {
		t.Fatal(err)
	}
	good := packageFile(t, packages, "good", 2, private, nil)
	if err = e.Apply(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	if e.Status().Current != "good" || e.Status().Previous != "base" || e.Status().Phase != "committed" {
		t.Fatal("update not committed")
	}
	if err = e.Apply(context.Background(), base); err == nil {
		t.Fatal("old signed release replay accepted")
	}
	runner.failVersion = "bad"
	bad := packageFile(t, packages, "bad", 3, private, nil)
	if err = e.Apply(context.Background(), bad); err == nil {
		t.Fatal("failed readiness accepted")
	}
	if e.Status().Current != "good" || e.Status().Phase != "rolled_back" {
		t.Fatal("rollback failed")
	}
	if _, err = os.Stat(filepath.Join(root, "releases", "base", "bin", "edge-web")); err != nil {
		t.Fatal("known good erased")
	}
	link, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil || filepath.Base(link) != "good" {
		t.Fatal("activation pointer not restored")
	}
}
func TestInterruptedUpdateRecoveryAndTerminalReconciliation(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	packages := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{space: 1 << 30}
	e, _ := Open(root, public, "linux", "arm64", runner)
	if err := e.Bootstrap(packageFile(t, packages, "base", 1, private, nil)); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"prepared", "activated", "verifying"} {
		journal := State{Schema: 1, Phase: phase, Current: "base", Previous: "base", Candidate: "candidate", Highest: 1}
		data, _ := json.Marshal(journal)
		if err := store.WriteAtomic(filepath.Join(root, "update-state.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(root, public, "linux", "arm64", runner)
		if err != nil {
			t.Fatal(err)
		}
		if err = reopened.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		if reopened.Status().Current != "base" || reopened.Status().Phase != "rolled_back" {
			t.Fatal("interrupted state not recovered")
		}
	}
	before := runner.restarts
	reopened, _ := Open(root, public, "linux", "arm64", runner)
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.restarts != before {
		t.Fatal("terminal transaction restarted services")
	}
}

type interruptedReader struct{}

func (interruptedReader) Read(p []byte) (int, error) { return copy(p, "partial"), io.ErrUnexpectedEOF }
func TestInterruptedDownloadAndDiskSpace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "release.zip")
	if SaveDownload(target, interruptedReader{}) == nil {
		t.Fatal("interrupted download accepted")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial download exposed as finished package")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("temporary download leaked")
	}
	if err := SaveDownload(target, bytes.NewBufferString("complete")); err != nil {
		t.Fatal(err)
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	e, _ := Open(t.TempDir(), public, "linux", "arm64", &fakeRunner{})
	if err := e.Bootstrap(packageFile(t, dir, "disk", 1, private, nil)); err == nil {
		t.Fatal("insufficient space not rejected")
	}
}

func TestRejectUnreadyRollbackBaseline(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	runner := &fakeRunner{space: 1 << 30, failVersion: "base"}
	root, packages := t.TempDir(), t.TempDir()
	e, err := Open(root, public, "linux", "arm64", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Bootstrap(packageFile(t, packages, "base", 1, private, nil)); err != nil {
		t.Fatal(err)
	}
	before := e.Status()
	if e.Apply(context.Background(), packageFile(t, packages, "next", 2, private, nil)) == nil {
		t.Fatal("unready rollback baseline accepted")
	}
	if e.Status() != before || runner.restarts != 0 {
		t.Fatal("baseline rejection changed state")
	}
	if _, err = os.Stat(filepath.Join(root, "releases", "next")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate was staged")
	}
}

func TestRejectMalformedUpdateJournals(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, journal := range []string{
		`not json`, `{"schema":2,"phase":"committed"}`,
		`{"schema":1,"phase":"unexpected"}`, `{"schema":1,"phase":"committed"}`,
		`{"schema":1,"phase":"prepared","current":"base","highest_sequence":1}`,
		`{"schema":1,"phase":"committed","current":"../../escape","highest_sequence":1}`,
	} {
		root := t.TempDir()
		if err := store.WriteAtomic(filepath.Join(root, "update-state.json"), []byte(journal), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(root, public, "linux", "arm64", &fakeRunner{}); err == nil {
			t.Fatalf("malformed journal accepted: %s", journal)
		}
	}
}

func TestServiceAssetsCannotEscapeApplicationBoundary(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "console", "edge-web.service"))
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateServiceAsset("edge-web", data); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		strings.Replace(string(data), "Wants=edge-updater.service", "Wants=sshd.service", 1),
		strings.Replace(string(data), "ReadWritePaths=/var/lib/edgebox/web", "ReadWritePaths=/etc", 1),
		strings.Replace(string(data), "User=edge-web", "User=root", 1),
		strings.Replace(string(data), "[Service]", "[Unit]", 1),
		string(data) + "\nExecStartPre=/bin/sh -c anything\n",
	} {
		if ValidateServiceAsset("edge-web", []byte(mutation)) == nil {
			t.Fatal("unsafe service asset accepted")
		}
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	bad := packageFile(t, t.TempDir(), "bad-service", 1, private, func(m *Manifest, files map[string][]byte) {
		name := "services/edge-web.service"
		files[name] = []byte(strings.Replace(string(data), "User=edge-web", "User=root", 1))
		sum := sha256.Sum256(files[name])
		for i := range m.Files {
			if m.Files[i].Name == name {
				m.Files[i].SHA256 = hex.EncodeToString(sum[:])
				m.Files[i].Size = int64(len(files[name]))
			}
		}
	})
	if _, err = Verify(bad, public, "linux", "arm64"); err == nil {
		t.Fatal("signed unsafe service accepted before activation")
	}
}
