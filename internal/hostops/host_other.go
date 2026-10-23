//go:build !linux

package hostops

import (
	"context"
	"errors"
	"net/http"

	"edgebox/internal/updater"
)

type Runner struct{}

func (Runner) FreeBytes(string) (uint64, error)     { return 0, errors.New("linux required") }
func (Runner) Restart(context.Context) error        { return errors.New("linux required") }
func (Runner) Verify(context.Context, string) error { return errors.New("linux required") }

type Broker struct {
	Engine *updater.Engine
	Inbox  string
}

func (*Broker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "linux required", http.StatusServiceUnavailable)
	})
}
func RecoverAP(context.Context) error { return errors.New("linux required") }
