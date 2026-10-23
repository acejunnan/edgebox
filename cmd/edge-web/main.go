package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"edgebox/internal/console"
	"golang.org/x/term"
)

var version = "2c-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge-web:", err)
		os.Exit(1)
	}
}
func run() error {
	mode := "serve"
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		mode = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	root := flag.String("state-dir", "/var/lib/edgebox/web", "owned Web state directory")
	socket := flag.String("updater-socket", "/run/edgebox-updater/control.sock", "fixed local updater socket")
	flag.Parse()
	if mode == "version" {
		fmt.Println(version)
		return nil
	}
	if mode == "password" || mode == "password-stdin" {
		var password string
		if mode == "password" {
			fmt.Fprint(os.Stderr, "New Web admin password (8–128 characters): ")
			data, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			fmt.Fprint(os.Stderr, "Confirm password: ")
			again, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			if string(data) != string(again) {
				return errors.New("passwords differ")
			}
			password = string(data)
		} else {
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				return errors.New("password input required")
			}
			password = scanner.Text()
		}
		if err := console.SetPassword(filepath.Join(*root, "auth.json"), password); err != nil {
			return err
		}
		fmt.Println("Web admin password updated. Restart edge-web to invalidate in-memory sessions.")
		return nil
	}
	if mode != "serve" {
		return errors.New("unknown mode")
	}
	auth, err := console.OpenAuth(filepath.Join(*root, "auth.json"), nil)
	if err != nil {
		return fmt.Errorf("local password provisioning required: %w", err)
	}
	nodes, err := console.OpenNodes(filepath.Join(*root, "nodes"))
	if err != nil {
		return err
	}
	app := console.Server{Auth: auth, Nodes: nodes, Backend: console.NewLocalClient(*socket), Version: version, Hosts: []string{"127.0.0.1", "localhost", "192.168.50.1"}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	for _, address := range []string{"127.0.0.1:8080", "192.168.50.1:8080"} {
		address := address
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				listener, err := net.Listen("tcp", address)
				if err != nil {
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
						continue
					}
				}
				server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
				go func() {
					<-ctx.Done()
					shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = server.Shutdown(shutdown)
				}()
				err = server.Serve(listener)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
				}
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}
