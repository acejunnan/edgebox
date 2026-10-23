package console

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/*
var assets embed.FS

type Backend interface {
	Snapshot(context.Context) (any, error)
	Action(context.Context, string, json.RawMessage) (any, error)
}
type Server struct {
	Auth    *Auth
	Nodes   *NodeStore
	Backend Backend
	Version string
	Hosts   []string
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	defer r.Body.Close()
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return errors.New("JSON content type required")
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"ready": true, "version": s.Version})
	})
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) {
			jsonResponse(w, 403, map[string]string{"error": "origin_rejected"})
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if readJSON(w, r, &body) != nil {
			jsonResponse(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		session, err := s.Auth.Login(body.Username, body.Password, peer)
		if err != nil {
			status := 401
			if err.Error() == "rate_limited" {
				status = 429
				w.Header().Set("Retry-After", "60")
			}
			jsonResponse(w, status, map[string]string{"error": err.Error()})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "edgebox_session", Value: session.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: session.Expires, MaxAge: int((8 * time.Hour).Seconds()), Secure: r.TLS != nil})
		jsonResponse(w, 200, session)
	})
	mux.HandleFunc("GET /api/session", s.protect(false, func(w http.ResponseWriter, r *http.Request, session Session) { jsonResponse(w, 200, session) }))
	mux.HandleFunc("POST /api/logout", s.protect(true, func(w http.ResponseWriter, r *http.Request, session Session) {
		cookie, _ := r.Cookie("edgebox_session")
		s.Auth.Logout(cookie.Value)
		http.SetCookie(w, &http.Cookie{Name: "edgebox_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		jsonResponse(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/password", s.protect(true, func(w http.ResponseWriter, r *http.Request, session Session) {
		var body struct {
			Current string `json:"current"`
			Next    string `json:"next"`
		}
		if readJSON(w, r, &body) != nil {
			jsonResponse(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		if err := s.Auth.ChangePassword(body.Current, body.Next); err != nil {
			jsonResponse(w, 400, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/nodes", s.protect(false, func(w http.ResponseWriter, r *http.Request, session Session) {
		n, err := s.Nodes.Snapshot()
		if err != nil {
			jsonResponse(w, 500, map[string]string{"error": "state_unavailable"})
			return
		}
		jsonResponse(w, 200, n)
	}))
	mux.HandleFunc("POST /api/nodes", s.protect(true, func(w http.ResponseWriter, r *http.Request, session Session) {
		var body struct {
			Expected uint64 `json:"expected"`
			Desired  Nodes  `json:"desired"`
		}
		if err := readJSON(w, r, &body); err != nil {
			jsonResponse(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		n, err := s.Nodes.Apply(body.Desired, body.Expected)
		if err != nil {
			status := 400
			if err.Error() == "revision_conflict" {
				status = 409
			}
			jsonResponse(w, status, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, 200, n)
	}))
	mux.HandleFunc("POST /api/nodes/rollback", s.protect(true, func(w http.ResponseWriter, r *http.Request, session Session) {
		var body struct {
			Expected uint64 `json:"expected"`
		}
		if readJSON(w, r, &body) != nil {
			jsonResponse(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		n, err := s.Nodes.Rollback(body.Expected)
		if err != nil {
			jsonResponse(w, 409, map[string]string{"error": "rollback_unavailable_or_conflict"})
			return
		}
		jsonResponse(w, 200, n)
	}))
	for _, route := range []string{"dashboard", "diagnostics", "bundle"} {
		route := route
		mux.HandleFunc("GET /api/"+route, s.protect(false, func(w http.ResponseWriter, r *http.Request, session Session) {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			snapshot, err := s.Backend.Snapshot(ctx)
			if err != nil {
				jsonResponse(w, 503, map[string]string{"error": "diagnostics_unavailable"})
				return
			}
			if fields, ok := snapshot.(map[string]any); ok {
				fields["version"] = s.Version
			}
			if route == "bundle" {
				w.Header().Set("Content-Disposition", "attachment; filename=edgebox-diagnostics.json")
			}
			jsonResponse(w, 200, snapshot)
		}))
	}
	for route, action := range map[string]string{"hotspot": "hotspot", "probes": "probes", "restart": "restart", "updates/apply": "update", "updates/download": "update_download"} {
		route, action := route, action
		mux.HandleFunc("POST /api/"+route, s.protect(true, func(w http.ResponseWriter, r *http.Request, session Session) {
			var body json.RawMessage
			if readJSON(w, r, &body) != nil {
				jsonResponse(w, 400, map[string]string{"error": "invalid_request"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			result, err := s.Backend.Action(ctx, action, body)
			if err != nil {
				jsonResponse(w, 400, map[string]string{"error": "operation_rejected_or_failed"})
				return
			}
			jsonResponse(w, 200, result)
		}))
	}
	mux.HandleFunc("POST /api/updates/upload", s.protect(true, func(w http.ResponseWriter, r *http.Request, session Session) {
		r.Body = http.MaxBytesReader(w, r.Body, 128<<20)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			jsonResponse(w, 400, map[string]string{"error": "upload_too_large_or_invalid"})
			return
		}
		file, _, err := r.FormFile("package")
		if err != nil {
			jsonResponse(w, 400, map[string]string{"error": "missing_package_file"})
			return
		}
		defer file.Close()
		uploadDir := "/var/lib/edgebox/web"
		if _, err := os.Stat(uploadDir); err != nil {
			uploadDir = os.TempDir()
		}
		tmpPath := filepath.Join(uploadDir, "upload.zip")
		out, err := os.Create(tmpPath)
		if err != nil {
			jsonResponse(w, 500, map[string]string{"error": "cannot_create_temp_file"})
			return
		}
		_, copyErr := io.Copy(out, file)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(tmpPath)
			jsonResponse(w, 500, map[string]string{"error": "upload_write_failed"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		reqData, _ := json.Marshal(map[string]string{"source": tmpPath})
		result, err := s.Backend.Action(ctx, "update_import", reqData)
		if err != nil {
			_ = os.Remove(tmpPath)
			jsonResponse(w, 400, map[string]string{"error": "invalid_or_untrusted_package"})
			return
		}
		jsonResponse(w, 200, result)
	}))
	mux.HandleFunc("GET /api/updates", s.protect(false, func(w http.ResponseWriter, r *http.Request, session Session) {
		result, err := s.Backend.Action(r.Context(), "update_status", nil)
		if err != nil {
			jsonResponse(w, 503, map[string]string{"error": "updater_unavailable"})
			return
		}
		jsonResponse(w, 200, result)
	}))
	sub, _ := fs.Sub(assets, "assets")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		allowed := false
		for _, candidate := range s.Hosts {
			if host == candidate {
				allowed = true
			}
		}
		if !allowed {
			jsonResponse(w, 403, map[string]string{"error": "host_rejected"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func (s *Server) protect(change bool, next func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("edgebox_session")
		if err != nil {
			jsonResponse(w, 401, map[string]string{"error": "login_required"})
			return
		}
		session, ok := s.Auth.Session(cookie.Value)
		if !ok {
			jsonResponse(w, 401, map[string]string{"error": "session_expired"})
			return
		}
		if change {
			csrf := r.Header.Get("X-CSRF-Token")
			if subtle.ConstantTimeCompare([]byte(csrf), []byte(session.CSRF)) != 1 {
				jsonResponse(w, 403, map[string]string{"error": "csrf_rejected"})
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) {
				jsonResponse(w, 403, map[string]string{"error": "origin_rejected"})
				return
			}
		}
		next(w, r, session)
	}
}
