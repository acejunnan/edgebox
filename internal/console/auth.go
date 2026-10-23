package console

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"edgebox/internal/store"
	"golang.org/x/crypto/argon2"
)

type passwordRecord struct {
	Schema    int    `json:"schema"`
	Algorithm string `json:"algorithm"`
	Salt      string `json:"salt"`
	Hash      string `json:"hash"`
}

type Session struct {
	Token   string    `json:"-"`
	CSRF    string    `json:"csrf"`
	Expires time.Time `json:"expires"`
}

type Auth struct {
	mu       sync.Mutex
	file     string
	record   passwordRecord
	sessions map[[32]byte]Session
	failures map[string][]time.Time
	now      func() time.Time
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 || len(password) > 128 || strings.ContainsAny(password, "\x00\r\n") {
		return errors.New("password must contain 8–128 characters")
	}
	common := map[string]bool{"password": true, "password1": true, "password123": true, "12345678": true, "123456789": true, "1234567890": true, "qwertyui": true, "qwerty123": true, "admin123": true, "admin1234": true, "abcdefgh": true, "letmein123": true, "edgebox123": true}
	if common[strings.ToLower(password)] {
		return errors.New("choose a less common password")
	}
	runes := []rune(password)
	repeated := true
	for _, r := range runes {
		if unicode.IsControl(r) {
			return errors.New("password must be readable")
		}
		if r != runes[0] {
			repeated = false
		}
	}
	if repeated || common[strings.ToLower(strings.TrimSpace(password))] {
		return errors.New("choose a less common password")
	}
	return nil
}

func newPassword(password string) (passwordRecord, error) {
	if err := ValidatePassword(password); err != nil {
		return passwordRecord{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return passwordRecord{}, err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return passwordRecord{1, "argon2id-v19-t3-m65536-p2", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)}, nil
}

func (r passwordRecord) matches(password string) bool {
	salt, e1 := base64.RawStdEncoding.DecodeString(r.Salt)
	hash, e2 := base64.RawStdEncoding.DecodeString(r.Hash)
	if r.Schema != 1 || r.Algorithm != "argon2id-v19-t3-m65536-p2" || e1 != nil || e2 != nil || len(salt) != 16 || len(hash) != 32 || len(password) > 128 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, hash) == 1
}

// SetPassword is a trusted local CLI operation; HTTP has no reset endpoint.
func SetPassword(file, password string) error {
	r, err := newPassword(password)
	if err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return store.WriteAtomic(file, data, 0600)
}

func OpenAuth(file string, now func() time.Time) (*Auth, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var r passwordRecord
	if err = json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(r.Salt)
	hash, e2 := base64.RawStdEncoding.DecodeString(r.Hash)
	if r.Schema != 1 || r.Algorithm != "argon2id-v19-t3-m65536-p2" || e1 != nil || e2 != nil || len(salt) != 16 || len(hash) != 32 {
		return nil, errors.New("invalid password record")
	}
	if now == nil {
		now = time.Now
	}
	return &Auth{file: file, record: r, sessions: map[[32]byte]Session{}, failures: map[string][]time.Time{}, now: now}, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), err
}

func (a *Auth) Login(username, password, peer string) (Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for key, times := range a.failures {
		fresh := times[:0]
		for _, at := range times {
			if now.Sub(at) < time.Minute {
				fresh = append(fresh, at)
			}
		}
		if len(fresh) == 0 {
			delete(a.failures, key)
		} else {
			a.failures[key] = fresh
		}
	}
	// Global bound supplements per-peer throttling; forwarded HTTP requests do
	// not get to choose their identity through X-Forwarded-For.
	if len(a.failures[peer]) >= 5 || len(a.failures["global"]) >= 20 {
		return Session{}, errors.New("rate_limited")
	}
	if username != "admin" || !a.record.matches(password) {
		a.failures[peer] = append(a.failures[peer], now)
		a.failures["global"] = append(a.failures["global"], now)
		return Session{}, errors.New("invalid_credentials")
	}
	delete(a.failures, peer)
	for key, session := range a.sessions {
		if !now.Before(session.Expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 32 {
		return Session{}, errors.New("session_limit")
	}
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	s := Session{token, csrf, now.Add(8 * time.Hour)}
	a.sessions[sha256.Sum256([]byte(token))] = s
	return s, nil
}

func (a *Auth) Session(token string) (Session, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := sha256.Sum256([]byte(token))
	s, ok := a.sessions[key]
	if !ok || !a.now().Before(s.Expires) {
		delete(a.sessions, key)
		return Session{}, false
	}
	return s, true
}
func (a *Auth) Logout(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sha256.Sum256([]byte(token)))
}
func (a *Auth) ChangePassword(current, next string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.record.matches(current) {
		return errors.New("current_password_invalid")
	}
	r, err := newPassword(next)
	if err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = store.WriteAtomic(a.file, data, 0600); err != nil {
		return err
	}
	a.record = r
	a.sessions = map[[32]byte]Session{}
	return nil
}
