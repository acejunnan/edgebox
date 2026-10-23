// Package updater verifies signed application releases and performs bounded
// activation/rollback. It never flashes an OS or interprets manifest commands.
package updater

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
)

const MaxPackage = int64(128 << 20)

var VersionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Manifest struct {
	Schema       int    `json:"schema"`
	Version      string `json:"version"`
	Sequence     uint64 `json:"sequence"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	ConfigSchema int    `json:"config_schema"`
	Files        []File `json:"files"`
}

func allowedFile(name string) bool {
	switch name {
	case "bin/edge-web", "bin/edge-agent", "bin/edge-transport", "bin/edge-dataplane", "schemas/config.json", "services/edge-web.service", "services/edge-agent.service":
		return true
	}
	return false
}
func (m Manifest) Validate(goos, arch string) error {
	if m.Schema != 1 || m.ConfigSchema != 1 || m.OS != goos || m.Arch != arch || !VersionPattern.MatchString(m.Version) || m.Sequence == 0 || len(m.Files) == 0 || len(m.Files) > 10 {
		return errors.New("incompatible release manifest")
	}
	var total int64
	seen := map[string]bool{}
	for _, file := range m.Files {
		decoded, err := hex.DecodeString(file.SHA256)
		if !allowedFile(file.Name) || path.Clean(file.Name) != file.Name || seen[file.Name] || err != nil || len(decoded) != 32 || file.Size <= 0 || file.Size > MaxPackage {
			return errors.New("invalid release file")
		}
		seen[file.Name] = true
		total += file.Size
	}
	if total > MaxPackage || !seen["bin/edge-web"] || !seen["services/edge-web.service"] {
		return errors.New("release exceeds limit or is missing required web assets")
	}
	return nil
}

type VerifiedPackage struct {
	Manifest Manifest
	Files    map[string][]byte
}

func readEntry(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) || file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
		return nil, errors.New("invalid archive entry")
	}
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("archive entry too large")
	}
	return data, nil
}
func Verify(archive string, key ed25519.PublicKey, goos, arch string) (VerifiedPackage, error) {
	info, err := os.Stat(archive)
	if err != nil {
		return VerifiedPackage{}, err
	}
	if info.Size() > MaxPackage || len(key) != ed25519.PublicKeySize {
		return VerifiedPackage{}, errors.New("invalid package or signing key")
	}
	r, err := zip.OpenReader(archive)
	if err != nil {
		return VerifiedPackage{}, err
	}
	defer r.Close()
	entries := map[string]*zip.File{}
	for _, file := range r.File {
		if _, ok := entries[file.Name]; ok {
			return VerifiedPackage{}, errors.New("duplicate archive entry")
		}
		entries[file.Name] = file
	}
	manifestEntry, ok := entries["manifest.json"]
	if !ok {
		return VerifiedPackage{}, errors.New("missing signed manifest")
	}
	signatureEntry, ok := entries["manifest.sig"]
	if !ok {
		return VerifiedPackage{}, errors.New("missing signature")
	}
	data, err := readEntry(manifestEntry, 16*1024)
	if err != nil {
		return VerifiedPackage{}, err
	}
	signature, err := readEntry(signatureEntry, 64)
	if err != nil {
		return VerifiedPackage{}, err
	}
	if !ed25519.Verify(key, data, signature) {
		return VerifiedPackage{}, errors.New("release authentication failed")
	}
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&m); err != nil {
		return VerifiedPackage{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return VerifiedPackage{}, errors.New("trailing manifest data")
	}
	if err = m.Validate(goos, arch); err != nil {
		return VerifiedPackage{}, err
	}
	if len(entries) != len(m.Files)+2 {
		return VerifiedPackage{}, errors.New("unexpected archive contents")
	}
	result := VerifiedPackage{m, map[string][]byte{}}
	for _, file := range m.Files {
		entry, ok := entries[file.Name]
		if !ok {
			return VerifiedPackage{}, errors.New("missing release file")
		}
		content, err := readEntry(entry, file.Size)
		if err != nil {
			return VerifiedPackage{}, err
		}
		sum := sha256.Sum256(content)
		if int64(len(content)) != file.Size || hex.EncodeToString(sum[:]) != file.SHA256 {
			return VerifiedPackage{}, fmt.Errorf("integrity check failed for %s", file.Name)
		}
		if file.Name == "services/edge-web.service" || file.Name == "services/edge-agent.service" {
			name := path.Base(file.Name)
			if err = ValidateServiceAsset(name[:len(name)-len(".service")], content); err != nil {
				return VerifiedPackage{}, err
			}
		}
		result.Files[file.Name] = content
	}
	return result, nil
}

// SaveDownload leaves no finished package after interruption or oversized input.
func SaveDownload(target string, reader io.Reader) error {
	file, err := os.CreateTemp(filepath.Dir(target), ".download-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	n, err := io.Copy(file, io.LimitReader(reader, MaxPackage+1))
	if err != nil {
		return err
	}
	if n > MaxPackage {
		return errors.New("download exceeds limit")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}
