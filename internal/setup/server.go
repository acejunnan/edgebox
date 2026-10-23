package setup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"
)

//go:embed assets/*
var assets embed.FS

type State struct {
	Busy   bool     `json:"busy"`
	Done   bool     `json:"done"`
	Events []string `json:"events"`
	Error  string   `json:"error,omitempty"`
	Result *Result  `json:"result,omitempty"`
}
type Server struct {
	Host      string
	Token     string
	SourceRef string
	Context   context.Context
	mu        sync.Mutex
	state     State
	inspected *Request
	ids       [2]Identity
	inspect   func(context.Context, Host) (Identity, error)
	install   func(context.Context, Request, Identity, Identity) (Result, error)
}

func NewServer(host, sourceRef string) *Server {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return &Server{Host: host, Token: hex.EncodeToString(b), SourceRef: sourceRef, Context: context.Background()}
}
func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.Header.Get("Content-Type") != "application/json" {
		return errors.New("JSON required")
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func withoutSecrets(r Request) Request {
	r.VPS.Password = ""
	r.VPS.KeyFile = ""
	r.VPS.Passphrase = ""
	r.Edge.Password = ""
	r.Edge.KeyFile = ""
	r.Edge.Passphrase = ""
	return r
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/inspect", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if decode(w, r, &req) != nil {
			respond(w, 400, map[string]string{"error": "输入格式无效"})
			return
		}
		if e := req.Validate(); e != nil {
			respond(w, 400, map[string]string{"error": e.Error()})
			return
		}
		s.mu.Lock()
		if s.state.Busy {
			s.mu.Unlock()
			respond(w, 409, map[string]string{"error": "安装正在进行"})
			return
		}
		s.state.Busy = true
		s.inspected = nil
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.state.Busy = false; s.mu.Unlock() }()
		inspect := s.inspect
		if inspect == nil {
			inspect = Inspect
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		a, e := inspect(ctx, req.VPS)
		if e != nil {
			respond(w, 400, map[string]string{"error": "VPS：" + e.Error()})
			return
		}
		b, e := inspect(ctx, req.Edge)
		if e != nil {
			respond(w, 400, map[string]string{"error": "小盒：" + e.Error()})
			return
		}
		public := withoutSecrets(req)
		s.mu.Lock()
		s.inspected = &public
		s.ids = [2]Identity{a, b}
		s.mu.Unlock()
		respond(w, 200, map[string]Identity{"vps": a, "edge": b})
	})
	mux.HandleFunc("POST /api/start", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Request   Request `json:"request"`
			Confirmed bool    `json:"confirmed"`
		}
		if decode(w, r, &input) != nil || !input.Confirmed {
			respond(w, 400, map[string]string{"error": "请先核对并确认两端 SSH 主机指纹"})
			return
		}
		if e := input.Request.Validate(); e != nil {
			respond(w, 400, map[string]string{"error": e.Error()})
			return
		}
		s.mu.Lock()
		if s.state.Busy || s.state.Done || s.inspected == nil || withoutSecrets(input.Request) != *s.inspected {
			s.mu.Unlock()
			respond(w, 409, map[string]string{"error": "请重新检查两端地址和主机指纹"})
			return
		}
		ids := s.ids
		s.inspected = nil
		s.state = State{Busy: true, Events: []string{"安装任务已开始，请保持本程序运行"}}
		s.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(s.Context, 90*time.Minute)
			defer cancel()
			emit := func(message string) { s.mu.Lock(); s.state.Events = append(s.state.Events, message); s.mu.Unlock() }
			install := s.install
			if install == nil {
				install = func(ctx context.Context, req Request, a, b Identity) (Result, error) {
					ref := s.SourceRef
					if ref == "" {
						var e error
						ref, e = ResolveSource(ctx)
						if e != nil {
							return Result{}, e
						}
					}
					return (Installer{SourceRef: ref, Emit: emit}).Install(ctx, req, a, b)
				}
			}
			result, e := install(ctx, input.Request, ids[0], ids[1])
			input.Request = withoutSecrets(input.Request)
			s.mu.Lock()
			defer s.mu.Unlock()
			s.state.Busy = false
			if e != nil {
				s.state.Error = e.Error()
				return
			}
			s.state.Done = true
			s.state.Result = &result
		}()
		respond(w, 202, map[string]bool{"started": true})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		respond(w, 200, s.state)
	})
	sub, _ := fs.Sub(assets, "assets")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'")
		if r.Host != s.Host {
			http.Error(w, "host rejected", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.Host {
			http.Error(w, "origin rejected", 403)
			return
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Setup-Token")), []byte(s.Token)) != 1 {
			http.Error(w, "session rejected", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
