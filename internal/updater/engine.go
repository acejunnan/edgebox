package updater

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"edgebox/internal/store"
)

type Runner interface {
	Restart(context.Context) error
	Verify(context.Context, string) error
	FreeBytes(string) (uint64, error)
}
type State struct {
	Schema    int       `json:"schema"`
	Phase     string    `json:"phase"`
	Current   string    `json:"current"`
	Previous  string    `json:"previous"`
	Candidate string    `json:"candidate,omitempty"`
	Highest   uint64    `json:"highest_sequence"`
	Reason    string    `json:"reason"`
	Updated   time.Time `json:"updated"`
}
type Engine struct {
	mu       sync.Mutex
	snapshot atomic.Value
	Root     string
	Key      ed25519.PublicKey
	OS, Arch string
	runner   Runner
	state    State
}

func Open(root string, key ed25519.PublicKey, goos, arch string, runner Runner) (*Engine, error) {
	if len(key) != 32 || runner == nil {
		return nil, errors.New("trusted signing key and runner required")
	}
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0755); err != nil {
		return nil, err
	}
	e := &Engine{Root: root, Key: append(ed25519.PublicKey(nil), key...), OS: goos, Arch: arch, runner: runner, state: State{Schema: 1, Phase: "unprovisioned", Updated: time.Now().UTC()}}
	e.snapshot.Store(e.state)
	data, err := os.ReadFile(filepath.Join(root, "update-state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &e.state); err != nil {
		return nil, err
	}
	if e.state.Schema != 1 {
		return nil, errors.New("invalid update journal")
	}
	for _, version := range []string{e.state.Current, e.state.Previous, e.state.Candidate} {
		if version != "" && !VersionPattern.MatchString(version) {
			return nil, errors.New("invalid journal release reference")
		}
	}
	switch e.state.Phase {
	case "committed", "rolled_back", "prepared", "activated", "verifying", "failed", "unprovisioned":
	default:
		return nil, errors.New("unknown update journal phase")
	}
	if e.state.Phase != "unprovisioned" && (e.state.Current == "" || e.state.Highest == 0) {
		return nil, errors.New("journal has no committed baseline")
	}
	if (e.state.Phase == "prepared" || e.state.Phase == "activated" || e.state.Phase == "verifying") && (e.state.Previous == "" || e.state.Candidate == "") {
		return nil, errors.New("incomplete update reconstruction data")
	}
	e.snapshot.Store(e.state)
	return e, nil
}
func (e *Engine) Status() State { return e.snapshot.Load().(State) }
func (e *Engine) persist() error {
	e.state.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(e.state, "", "  ")
	if err != nil {
		return err
	}
	if err = store.WriteAtomic(filepath.Join(e.Root, "update-state.json"), data, 0600); err != nil {
		return err
	}
	e.snapshot.Store(e.state)
	return nil
}
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (e *Engine) activate(version string) error {
	if !VersionPattern.MatchString(version) {
		return errors.New("invalid activation reference")
	}
	info, err := os.Lstat(filepath.Join(e.Root, "releases", version))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid owned release directory")
	}
	temp := filepath.Join(e.Root, ".current-next")
	if err := os.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(filepath.Join("releases", version), temp); err != nil {
		return err
	}
	// Windows is a development/test host, not an updater deployment target.
	// Its rename cannot replace directory symlinks. Linux activation remains
	// one atomic rename; Windows fixtures remove only our verified symlink.
	if runtime.GOOS == "windows" {
		current := filepath.Join(e.Root, "current")
		if info, err := os.Lstat(current); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return errors.New("activation pointer is not a symlink")
			}
			if err := os.Remove(current); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(temp, filepath.Join(e.Root, "current")); err != nil {
		return err
	}
	return syncDir(e.Root)
}
func (e *Engine) stage(p VerifiedPackage) error {
	var required uint64
	for _, file := range p.Manifest.Files {
		required += uint64(file.Size)
	}
	free, err := e.runner.FreeBytes(e.Root)
	if err != nil {
		return err
	}
	if free < required*2+32*1024*1024 {
		return errors.New("insufficient update disk space")
	}
	dir := filepath.Join(e.Root, "releases", p.Manifest.Version)
	if _, err := os.Lstat(dir); err == nil {
		return errors.New("release version already exists")
	}
	stage, err := os.MkdirTemp(e.Root, ".release-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, data := range p.Files {
		mode := os.FileMode(0644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0755
		}
		if err = store.WriteAtomic(filepath.Join(stage, filepath.FromSlash(name)), data, mode); err != nil {
			return err
		}
	}
	manifestData, _ := json.Marshal(p.Manifest)
	if err = store.WriteAtomic(filepath.Join(stage, "installed-manifest.json"), manifestData, 0644); err != nil {
		return err
	}
	// Releases contain public executable/schema assets. The staging directory
	// starts private, then becomes traversable by the unprivileged services.
	if err = filepath.WalkDir(stage, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(name, 0755)
		}
		return nil
	}); err != nil {
		return err
	}
	if err = os.Rename(stage, dir); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dir))
}
func (e *Engine) Bootstrap(archive string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Phase != "unprovisioned" {
		return errors.New("already provisioned")
	}
	p, err := Verify(archive, e.Key, e.OS, e.Arch)
	if err != nil {
		return err
	}
	if err = e.stage(p); err != nil {
		return err
	}
	if err = e.activate(p.Manifest.Version); err != nil {
		return err
	}
	e.state = State{Schema: 1, Phase: "committed", Current: p.Manifest.Version, Highest: p.Manifest.Sequence, Reason: "trusted_local_bootstrap"}
	return e.persist()
}
func (e *Engine) rollback(ctx context.Context, reason string) error {
	previous := e.state.Previous
	if previous == "" {
		e.state.Phase = "failed"
		e.state.Reason = "no_known_good_release"
		_ = e.persist()
		return errors.New("no known good release")
	}
	if err := e.activate(previous); err != nil {
		return err
	}
	if err := e.runner.Restart(ctx); err != nil {
		return err
	}
	if err := e.runner.Verify(ctx, previous); err != nil {
		e.state.Phase = "failed"
		e.state.Reason = "rollback_readiness_failed"
		_ = e.persist()
		return err
	}
	e.state.Current = previous
	e.state.Candidate = ""
	e.state.Phase = "rolled_back"
	e.state.Reason = reason
	return e.persist()
}
func (e *Engine) Recover(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch e.state.Phase {
	case "prepared", "activated", "verifying":
		return e.rollback(ctx, "interrupted_update_restored_previous")
	case "committed", "rolled_back", "unprovisioned":
		return nil
	default:
		return errors.New("update needs local recovery")
	}
}
func (e *Engine) Apply(ctx context.Context, archive string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Phase != "committed" && e.state.Phase != "rolled_back" {
		return errors.New("update not ready")
	}
	p, err := Verify(archive, e.Key, e.OS, e.Arch)
	if err != nil {
		return err
	}
	if p.Manifest.Sequence <= e.state.Highest {
		return errors.New("replayed or older release")
	}
	// Never designate an unready installation as the rollback baseline.
	if err = e.runner.Verify(ctx, e.state.Current); err != nil {
		return errors.New("current release and management are not ready")
	}
	if err = e.stage(p); err != nil {
		return err
	}
	e.state.Previous = e.state.Current
	e.state.Candidate = p.Manifest.Version
	e.state.Phase = "prepared"
	e.state.Reason = "signature_and_hash_verified"
	if err = e.persist(); err != nil {
		return err
	}
	if err = e.activate(p.Manifest.Version); err == nil {
		e.state.Phase = "activated"
		err = e.persist()
	}
	if err == nil {
		err = e.runner.Restart(ctx)
	}
	if err == nil {
		e.state.Phase = "verifying"
		err = e.persist()
	}
	if err == nil {
		err = e.runner.Verify(ctx, p.Manifest.Version)
	}
	if err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if rollbackErr := e.rollback(rollbackCtx, "candidate_readiness_failed"); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	e.state.Current = p.Manifest.Version
	e.state.Candidate = ""
	e.state.Highest = p.Manifest.Sequence
	e.state.Phase = "committed"
	e.state.Reason = "service_and_management_verified"
	return e.persist()
}
