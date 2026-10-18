package server

// Active-session allowlist: deleting an identity is durable revocation. A
// separate, owner-only file avoids coupling auth to application DB migrations.
// One server process owns STORE_DIR, just like the WhatsApp device store.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxActiveSessions = 10000

type sessionState struct {
	mu         sync.Mutex
	loaded     bool
	err        error
	active     map[string]int64
	generation string
}

type sessionRegistry struct {
	Generation string           `json:"generation"`
	Active     map[string]int64 `json:"active"`
}

func sessionIdentity(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (app *App) loadSessionsLocked() {
	s := &app.sessions
	if s.loaded {
		return
	}
	s.loaded = true
	s.active = make(map[string]int64)
	s.generation = app.sessionMACFor("auth-generation")
	if app.sessionKeyDir == "" {
		return
	}
	f, err := os.Open(filepath.Join(app.sessionKeyDir, "sessions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.err = err
		return
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 2<<20))
	if err != nil {
		s.err = err
		return
	}
	var registry sessionRegistry
	if err = json.Unmarshal(b, &registry); err != nil {
		s.err = err
		return
	}
	if registry.Active == nil || len(registry.Active) > maxActiveSessions {
		s.err = errors.New("invalid session registry")
		return
	}
	if registry.Generation == s.generation {
		s.active = registry.Active
	} else {
		s.err = app.saveSessionsLocked()
	}
}

func (app *App) saveSessionsLocked() error {
	if app.sessionKeyDir == "" {
		return nil
	}
	b, err := json.Marshal(sessionRegistry{Generation: app.sessions.generation, Active: app.sessions.active})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(app.sessionKeyDir, ".sessions-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(app.sessionKeyDir, "sessions.json")); err != nil {
		return err
	}
	d, err := os.Open(app.sessionKeyDir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func (app *App) registerSession(value string) bool {
	s := &app.sessions
	s.mu.Lock()
	defer s.mu.Unlock()
	app.loadSessionsLocked()
	if s.err != nil {
		return false
	}
	now := time.Now().Unix()
	for id, expiry := range s.active {
		if expiry <= now {
			delete(s.active, id)
		}
	}
	if len(s.active) >= maxActiveSessions {
		return false
	}
	s.active[sessionIdentity(value)] = now + int64(sessionTTL/time.Second)
	s.err = app.saveSessionsLocked()
	return s.err == nil
}

func (app *App) sessionActive(value string) bool {
	s := &app.sessions
	s.mu.Lock()
	defer s.mu.Unlock()
	app.loadSessionsLocked()
	return s.err == nil && s.active[sessionIdentity(value)] > time.Now().Unix()
}

func (app *App) revokeSession(value string) error {
	s := &app.sessions
	s.mu.Lock()
	defer s.mu.Unlock()
	app.loadSessionsLocked()
	if s.err != nil {
		return s.err
	}
	id := sessionIdentity(value)
	if _, exists := s.active[id]; !exists {
		return nil
	}
	delete(s.active, id)
	for id, expiry := range s.active {
		if expiry <= time.Now().Unix() {
			delete(s.active, id)
		}
	}
	s.err = app.saveSessionsLocked()
	return s.err
}
