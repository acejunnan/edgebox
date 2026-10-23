// Package store provides the small, filesystem-backed durable state layout for
// EdgeBox. It intentionally has no database or background activity.
package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const temporaryPrefix = ".edgebox-tmp-"

var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

var ownedDirectories = [...]string{"current", "factory", "identity", "previous", "transactions"}

type Store struct {
	root string
	mu   sync.Mutex
}

func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("state store root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve state store root: %w", err)
	}
	if err := mkdirAllDurable(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create state store root: %w", err)
	}
	for _, relative := range ownedDirectories {
		if err := mkdirAllDurable(filepath.Join(absolute, relative), 0o700); err != nil {
			return nil, fmt.Errorf("create owned state directory %q: %w", relative, err)
		}
	}
	return &Store{root: absolute}, nil
}

func (s *Store) WriteFactory(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeOnce(filepath.Join("factory", "config.json"), data)
}

func (s *Store) ReadFactory() ([]byte, error) {
	return s.read(filepath.Join("factory", "config.json"))
}

func (s *Store) CommitCurrent(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentPath := s.path(filepath.Join("current", "config.json"))
	current, err := os.ReadFile(currentPath)
	switch {
	case err == nil && bytes.Equal(current, data):
		return nil
	case err == nil:
		if err := writeAtomic(s.path(filepath.Join("previous", "config.json")), current, 0o600); err != nil {
			return fmt.Errorf("preserve previous known-good configuration: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		// The first commit has no previous known-good configuration.
	default:
		return fmt.Errorf("read current configuration: %w", err)
	}
	if err := writeAtomic(currentPath, data, 0o600); err != nil {
		return fmt.Errorf("commit current configuration: %w", err)
	}
	return nil
}

func (s *Store) ReadCurrent() ([]byte, error) {
	return s.read(filepath.Join("current", "config.json"))
}

func (s *Store) ReadPrevious() ([]byte, error) {
	return s.read(filepath.Join("previous", "config.json"))
}

func (s *Store) WriteDeviceID(deviceID string) error {
	if !safeNamePattern.MatchString(deviceID) {
		return errors.New("device identity must use only letters, digits, dot, underscore, and hyphen")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeOnce(filepath.Join("identity", "device-id"), []byte(deviceID+"\n"))
}

func (s *Store) ReadDeviceID() (string, error) {
	data, err := s.read(filepath.Join("identity", "device-id"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *Store) DeleteJournal(id string) error {
	if !safeNamePattern.MatchString(id) {
		return errors.New("transaction journal id is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.journalPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete transaction journal: %w", err)
	}
	return syncDirectory(filepath.Dir(s.journalPath(id)))
}

// Recover removes only abandoned atomic-write temporary files and reports
// deterministic reconciliation actions for durable transaction journals. It
// never performs network rollback or deletes non-store state.
func (s *Store) Recover() (Recovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var recovery Recovery
	for _, relativeDirectory := range ownedDirectories {
		directory := s.path(relativeDirectory)
		entries, err := os.ReadDir(directory)
		if err != nil {
			return Recovery{}, fmt.Errorf("inspect owned state directory %q: %w", relativeDirectory, err)
		}
		removed := false
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), temporaryPrefix) {
				continue
			}
			filePath := filepath.Join(directory, entry.Name())
			if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return Recovery{}, fmt.Errorf("remove abandoned store temporary file: %w", err)
			}
			recovery.RemovedTemporaryFiles = append(recovery.RemovedTemporaryFiles, filepath.Join(relativeDirectory, entry.Name()))
			removed = true
		}
		if removed {
			if err := syncDirectory(directory); err != nil {
				return Recovery{}, fmt.Errorf("sync recovered state directory %q: %w", relativeDirectory, err)
			}
		}
	}

	journals, err := s.readJournals()
	if err != nil {
		return Recovery{}, err
	}
	for _, journal := range journals {
		recovery.Reconciliations = append(recovery.Reconciliations, Reconciliation{
			Journal: journal,
			Action:  reconciliationAction(journal.Phase()),
		})
	}
	sort.Strings(recovery.RemovedTemporaryFiles)
	return recovery, nil
}

func (s *Store) writeOnce(relative string, data []byte) error {
	target := s.path(relative)
	existing, err := os.ReadFile(target)
	switch {
	case err == nil && bytes.Equal(existing, data):
		return nil
	case err == nil:
		return fmt.Errorf("%s already exists with different content", relative)
	case errors.Is(err, os.ErrNotExist):
		return writeAtomic(target, data, 0o600)
	default:
		return fmt.Errorf("read %s: %w", relative, err)
	}
}

func (s *Store) read(relative string) ([]byte, error) {
	data, err := os.ReadFile(s.path(relative))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relative, err)
	}
	return data, nil
}

func (s *Store) path(relative string) string {
	return filepath.Join(s.root, relative)
}

func (s *Store) journalPath(id string) string {
	return s.path(filepath.Join("transactions", id+".json"))
}

func writeAtomic(target string, data []byte, mode fs.FileMode) (returnErr error) {
	directory := filepath.Dir(target)
	if err := mkdirAllDurable(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, temporaryPrefix)
	if err != nil {
		return fmt.Errorf("create atomic temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		if returnErr != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set atomic temporary file mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write atomic temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync atomic temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close atomic temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("replace state file atomically: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

// WriteAtomic persists a caller-owned file with the same durability guarantees
// as store state. Callers must validate their fixed target and file mode.
func WriteAtomic(target string, data []byte, mode fs.FileMode) error {
	return writeAtomic(target, data, mode)
}

// mkdirAllDurable creates each missing directory component and syncs its parent
// on Linux so a sudden power loss cannot lose a newly created directory entry
// after a later file was reported durable.
func mkdirAllDurable(directory string, mode fs.FileMode) error {
	directory = filepath.Clean(directory)
	missing := make([]string, 0, 4)
	current := directory
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("no existing ancestor for %s", directory)
		}
		current = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		created := missing[i]
		if err := os.Mkdir(created, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := syncDirectory(filepath.Dir(created)); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
