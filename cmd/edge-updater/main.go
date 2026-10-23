package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"edgebox/internal/hostops"
	"edgebox/internal/updater"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge-updater:", err)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("root", "/opt/edgebox", "owned release root")
	keyPath := flag.String("trusted-key", "/etc/edgebox/release-signing.pub", "root-owned trusted release public key")
	bootstrap := flag.String("bootstrap", "", "trusted first signed package")
	apply := flag.String("apply", "", "apply an approved signed package locally")
	socket := flag.String("socket", "/run/edgebox-updater/control.sock", "local privileged fixed-operation socket")
	inbox := flag.String("inbox", "/var/lib/edgebox/updates/inbox", "root-owned approved release inbox")
	flag.Parse()
	info, err := os.Lstat(*keyPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return errors.New("trusted key must be a non-writable regular file")
	}
	data, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != 32 {
		return errors.New("invalid trusted release public key")
	}
	engine, err := updater.Open(*root, ed25519.PublicKey(key), runtime.GOOS, runtime.GOARCH, hostops.Runner{})
	if err != nil {
		return err
	}
	if *bootstrap != "" {
		return engine.Bootstrap(*bootstrap)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	recovery, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = hostops.RecoverAP(recovery)
	if err == nil {
		err = engine.Recover(recovery)
	}
	if err == nil {
		err = updater.RecoverDownloads(*inbox)
	}
	cancel()
	if err != nil {
		return err
	}
	if *apply != "" {
		applyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		return engine.Apply(applyCtx, *apply)
	}
	if info, err := os.Lstat(*socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("refusing to remove non-socket control path")
		}
		if err = os.Remove(*socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(*socket, 0660); err != nil {
		return err
	}
	broker := &hostops.Broker{Engine: engine, Inbox: *inbox}
	server := &http.Server{Handler: broker.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
