// edge-setup runs only on the operator's workstation and opens the local wizard.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"

	"edgebox/internal/setup"
)

var sourceRef string // release builds pin this to the published source commit
func main() {
	noBrowser := flag.Bool("no-browser", false, "print local URL without opening browser")
	ref := flag.String("source-ref", sourceRef, "published 40-character source commit (default: resolve main)")
	flag.Parse()
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		fmt.Fprintln(os.Stderr, "Cannot open local wizard:", e)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	app := setup.NewServer(listener.Addr().String(), *ref)
	app.Context = ctx
	server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 40 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second}
	url := "http://" + listener.Addr().String() + "/#" + app.Token
	fmt.Println("EdgeBox 安装向导：", url)
	if !*noBrowser {
		var command *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		case "darwin":
			command = exec.Command("open", url)
		default:
			command = exec.Command("xdg-open", url)
		}
		if e := command.Start(); e != nil {
			fmt.Fprintln(os.Stderr, "请在本机浏览器打开上方地址")
		}
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if e = server.Serve(listener); e != nil && e != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
