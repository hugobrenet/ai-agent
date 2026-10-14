package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
)

const (
	// identityCacheLifetime bounds the reuse of a whoami answer: the requests
	// of a conversation reuse it, and a change of the account or of the claim
	// rules of the target applies within a minute.
	identityCacheLifetime = time.Minute
	// identityRefusalMemory is how long a refused token is refused again
	// without asking the MCP: a client looping on it reaches the target at
	// most once per period.
	identityRefusalMemory = 5 * time.Second
	// identityCacheEntries bounds the memory of the cache. When it is full of
	// live entries, new answers are not cached.
	identityCacheEntries = 4096
)

// identityCache keeps the whoami answers of the MCP in the memory of the
// process only, never on disk. An answer is an identity, never a credential:
// the cache holds no token, and its keys are hashes of the token and of the
// headers sent with it. Concurrent requests of the same key share one
// whoami; an unavailable MCP or target is never cached.
type identityCache struct {
	mu      sync.Mutex
	now     func() time.Time
	max     int
	entries map[string]*cachedIdentity
}

type cachedIdentity struct {
	pending bool
	done    chan struct{} // closed when the whoami ends
	data    []byte        // the JSON answer of the MCP
	err     error
	// retry tells waiters that the whoami ended with the context of the
	// request that ran it, which says nothing about the token.
	retry bool
	until time.Time // reuse limit of the answer or of the refusal
}

func newIdentityCache() *identityCache {
	return &identityCache{now: time.Now, max: identityCacheEntries, entries: make(map[string]*cachedIdentity)}
}

// identityCacheKey hashes the token and the headers forwarded with it, such
// as the om3 cluster: an identity confirmed on one target is never reused for
// another.
func identityCacheKey(raw string, headers http.Header) string {
	h := sha256.New()
	h.Write([]byte(raw))
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	slices.Sort(names)
	for _, name := range names {
		h.Write([]byte("\x00" + name + "\x00" + strings.Join(headers.Values(name), "\x00")))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// get returns the cached answer or refusal of key while it may be reused,
// waits for the whoami of key in progress, or runs whoami.
func (c *identityCache) get(ctx context.Context, key string, whoami func(context.Context) ([]byte, error)) ([]byte, error) {
	for {
		c.mu.Lock()
		e := c.entries[key]
		if e != nil && !e.pending && c.now().Before(e.until) {
			c.mu.Unlock()
			return e.data, e.err
		}
		if e != nil && e.pending {
			c.mu.Unlock()
			select {
			case <-e.done:
			case <-ctx.Done():
				return nil, auth.ErrVerificationUnavailable
			}
			if e.retry {
				continue
			}
			return e.data, e.err
		}
		e = &cachedIdentity{pending: true, done: make(chan struct{})}
		cached := c.reserve(key, e)
		c.mu.Unlock()

		data, err := whoami(ctx)

		c.mu.Lock()
		e.pending, e.data, e.err, e.retry = false, data, err, err != nil && ctx.Err() != nil
		switch {
		case err == nil:
			e.until = c.reuseLimit(data)
		case errors.Is(err, auth.ErrInvalidToken):
			e.until = c.now().Add(identityRefusalMemory)
		}
		if cached && !c.now().Before(e.until) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		close(e.done)
		return data, err
	}
}

// reuseLimit is the end of the reuse of an answer: a minute, and never past
// the expiry the MCP reports. An answer without expiry is not reused.
func (c *identityCache) reuseLimit(data []byte) time.Time {
	var answer struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	now := c.now()
	if json.Unmarshal(data, &answer) != nil || answer.ExpiresAt.IsZero() {
		return now
	}
	if limit := now.Add(identityCacheLifetime); limit.Before(answer.ExpiresAt) {
		return limit
	}
	return answer.ExpiresAt
}

// reserve records the pending whoami e of key, unless the cache is full of
// live entries. Called with c.mu held.
func (c *identityCache) reserve(key string, e *cachedIdentity) bool {
	if _, replace := c.entries[key]; !replace && len(c.entries) >= c.max {
		now := c.now()
		for k, old := range c.entries {
			if !old.pending && !now.Before(old.until) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.max {
			return false
		}
	}
	c.entries[key] = e
	return true
}
