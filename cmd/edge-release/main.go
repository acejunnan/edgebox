package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"edgebox/internal/updater"
)

type files []string

func (f *files) String() string     { return strings.Join(*f, ",") }
func (f *files) Set(v string) error { *f = append(*f, v); return nil }
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge-release:", err)
		os.Exit(1)
	}
}
func run() error {
	generate := flag.Bool("keygen", false, "generate a dedicated release-signing key pair outside Git")
	private := flag.String("private-key", "", "raw Ed25519 private-key path")
	public := flag.String("public-key", "", "base64 Ed25519 public-key path")
	output := flag.String("output", "", "new signed zip package")
	version := flag.String("version", "", "release version")
	sequence := flag.Uint64("sequence", 0, "monotonic release sequence")
	arch := flag.String("arch", "arm64", "release architecture")
	var entries files
	flag.Var(&entries, "file", "approved package name=local artifact path")
	flag.Parse()
	if *private == "" {
		return errors.New("private-key path required")
	}
	if *generate {
		if *public == "" {
			return errors.New("public-key path required")
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		for _, file := range []struct {
			path string
			data []byte
		}{{*private, priv}, {*public, []byte(base64.StdEncoding.EncodeToString(pub) + "\n")}} {
			f, err := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, writeErr := f.Write(file.data)
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}
	key, err := os.ReadFile(*private)
	if err != nil {
		return err
	}
	if len(key) != 64 {
		return errors.New("invalid signing key")
	}
	m := updater.Manifest{Schema: 1, Version: *version, Sequence: *sequence, OS: "linux", Arch: *arch, ConfigSchema: 1}
	contents := map[string][]byte{}
	for _, entry := range entries {
		name, file, ok := strings.Cut(entry, "=")
		if !ok {
			return errors.New("invalid file mapping")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		m.Files = append(m.Files, updater.File{Name: name, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))})
		contents[name] = data
	}
	if err = m.Validate("linux", *arch); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	contents["manifest.json"] = data
	contents["manifest.sig"] = ed25519.Sign(key, data)
	f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	for name, content := range contents {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0644)
		if strings.HasPrefix(name, "bin/") {
			header.SetMode(0755)
		}
		writer, err := z.CreateHeader(header)
		if err != nil {
			f.Close()
			return err
		}
		if _, err = writer.Write(content); err != nil {
			f.Close()
			return err
		}
	}
	if err = z.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
