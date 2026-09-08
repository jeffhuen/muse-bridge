// Package keys caches the minted Model API key. The cache is the only
// state shared across requests; a mutex (not channels) guards it because
// the critical sections are trivial.
package keys

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/auth"
)

// Provider abstracts key resolution so the proxy can be tested without
// network or credentials.
type Provider interface {
	CurrentKey() (string, error)
	Invalidate()
}

// Store mints via the stored identity, falling back to static direct
// keys, and caches the winner for ttl.
type Store struct {
	mu     sync.Mutex
	key    string
	at     time.Time
	ttl    time.Duration
	client *http.Client
}

// NewStore returns a Provider backed by live Meta endpoints.
func NewStore(client *http.Client, ttl time.Duration) *Store {
	return &Store{client: client, ttl: ttl}
}

// CurrentKey returns the cached key when fresh, else resolves a new one.
// The lock is held across minting so concurrent requests never mint twice.
func (s *Store) CurrentKey() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != "" && time.Since(s.at) < s.ttl {
		return s.key, nil
	}
	log.Println("resolving key")
	key, err := s.resolve()
	if err != nil {
		return "", err
	}
	s.key, s.at = key, time.Now()
	return key, nil
}

// Invalidate drops the cached key so the next call re-resolves.
// Callers use this after an upstream 401.
func (s *Store) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key = ""
}

func (s *Store) resolve() (string, error) {
	if ident, src := auth.LoadIdentity(); ident != "" {
		key, err := auth.MintAPIKey(s.client, ident)
		if err != nil {
			log.Printf("mint via %s failed: %s", src, err)
		} else if key == "" {
			log.Printf("mint via %s returned no key", src)
		} else {
			log.Printf("minted key via %s", src)
			return key, nil
		}
	}
	for _, dk := range auth.LoadDirectKeys() {
		log.Printf("using direct key from %s", dk.From)
		return dk.Key, nil
	}
	return "", auth.ErrNoCredential
}
