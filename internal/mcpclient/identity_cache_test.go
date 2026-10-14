package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
)

// fakeClock is the clock of an identity cache under test.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// whoamiServer is a fake MCP whose whoami answers status, with an identity
// expiring after lifetime when status is 200, and counts its requests.
type whoamiServer struct {
	calls    atomic.Int32
	status   atomic.Int32
	lifetime atomic.Int64
	release  chan struct{}
}

func newWhoamiClient(t *testing.T) (*Client, *whoamiServer, *fakeClock) {
	t.Helper()
	s := &whoamiServer{}
	s.status.Store(http.StatusOK)
	s.lifetime.Store(int64(time.Hour))
	endpoint := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if s.release != nil {
			<-s.release
		}
		if status := int(s.status.Load()); status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"subject":%q,"cluster_id":%q,"expires_at":%q}`, r.Header.Get("Authorization"), r.Header.Get(testClusterHeader),
			time.Now().Add(time.Duration(s.lifetime.Load())).UTC().Format(time.RFC3339Nano))
	}))
	client, err := New(endpoint, testClusterHeader, testNodeHeader)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Now()}
	client.identities.now = clock.Now
	return client, s, clock
}

type whoamiAnswer struct {
	Subject   string `json:"subject"`
	ClusterID string `json:"cluster_id"`
}

func ask(t *testing.T, c *Client, token, cluster string) (whoamiAnswer, error) {
	t.Helper()
	var answer whoamiAnswer
	err := c.WhoAmI(auth.WithMCPHeaders(t.Context(), targetHeaders(cluster, "")), token, &answer)
	return answer, err
}

func TestWhoAmIReusesAnAnswerForAtMostAMinute(t *testing.T) {
	c, s, clock := newWhoamiClient(t)
	for range 3 {
		if answer, err := ask(t, c, "token-a", "cluster-a"); err != nil || answer.Subject != "Bearer token-a" || answer.ClusterID != "cluster-a" {
			t.Fatalf("answer=%+v err=%v", answer, err)
		}
	}
	if s.calls.Load() != 1 {
		t.Fatalf("%d whoami for three requests of the same token", s.calls.Load())
	}
	clock.Add(61 * time.Second)
	if _, err := ask(t, c, "token-a", "cluster-a"); err != nil || s.calls.Load() != 2 {
		t.Fatalf("answer reused after a minute: %d whoami", s.calls.Load())
	}
}

func TestWhoAmICacheSeparatesTokensAndTargets(t *testing.T) {
	c, s, _ := newWhoamiClient(t)
	for _, req := range [][2]string{{"token-a", "cluster-a"}, {"token-a", "cluster-b"}, {"token-b", "cluster-a"}, {"token-a", "cluster-a"}} {
		answer, err := ask(t, c, req[0], req[1])
		if err != nil || answer.Subject != "Bearer "+req[0] || answer.ClusterID != req[1] {
			t.Fatalf("request %v got %+v %v", req, answer, err)
		}
	}
	if s.calls.Load() != 3 {
		t.Fatalf("%d whoami, want one per token and cluster", s.calls.Load())
	}
}

func TestWhoAmINeverReusesAnAnswerPastItsExpiry(t *testing.T) {
	c, s, clock := newWhoamiClient(t)
	s.lifetime.Store(int64(10 * time.Second))
	_, _ = ask(t, c, "token-a", "cluster-a")
	clock.Add(11 * time.Second)
	if _, err := ask(t, c, "token-a", "cluster-a"); err != nil || s.calls.Load() != 2 {
		t.Fatalf("answer reused past its expiry: %d whoami", s.calls.Load())
	}
}

func TestWhoAmIRemembersRefusalsButNotOutages(t *testing.T) {
	c, s, clock := newWhoamiClient(t)
	s.status.Store(http.StatusUnauthorized)
	for range 3 {
		if _, err := ask(t, c, "refused", "cluster-a"); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("err = %v", err)
		}
	}
	if s.calls.Load() != 1 {
		t.Fatalf("a client looping on a refused token reached the MCP %d times", s.calls.Load())
	}
	clock.Add(5 * time.Second)
	s.status.Store(http.StatusOK)
	if _, err := ask(t, c, "refused", "cluster-a"); err != nil || s.calls.Load() != 2 {
		t.Fatalf("refusal remembered too long: %v", err)
	}
	s.status.Store(http.StatusBadGateway)
	for range 2 {
		if _, err := ask(t, c, "outage", "cluster-a"); !errors.Is(err, auth.ErrVerificationUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if s.calls.Load() != 4 {
		t.Fatalf("an unavailable verification was cached: %d whoami", s.calls.Load())
	}
}

func TestWhoAmIConcurrentRequestsShareOneCheck(t *testing.T) {
	c, s, _ := newWhoamiClient(t)
	s.release = make(chan struct{})
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := ask(t, c, "token-a", "cluster-a"); err != nil {
				t.Error(err)
			}
		})
	}
	for s.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(s.release)
	wg.Wait()
	if s.calls.Load() != 1 {
		t.Fatalf("%d whoami for concurrent requests", s.calls.Load())
	}
}

func TestWhoAmIWaitersRetryWhenTheFirstRequestGivesUp(t *testing.T) {
	c, s, _ := newWhoamiClient(t)
	s.release = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		_ = c.WhoAmI(auth.WithMCPHeaders(ctx, targetHeaders("cluster-a", "")), "token-a", &whoamiAnswer{})
	}()
	for s.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	done := make(chan error)
	go func() {
		_, err := ask(t, c, "token-a", "cluster-a")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	close(s.release)
	if err := <-done; err != nil {
		t.Fatalf("waiter inherited the cancellation of another request: %v", err)
	}
}

func TestWhoAmICacheStaysBounded(t *testing.T) {
	c, s, clock := newWhoamiClient(t)
	c.identities.max = 2
	for _, token := range []string{"a", "b", "c", "c"} {
		if _, err := ask(t, c, token, "cluster-a"); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.identities.entries) != 2 || s.calls.Load() != 4 {
		t.Fatalf("full cache: %d entries, %d whoami", len(c.identities.entries), s.calls.Load())
	}
	clock.Add(2 * time.Minute)
	if _, err := ask(t, c, "c", "cluster-a"); err != nil || len(c.identities.entries) != 1 {
		t.Fatalf("expired entries not purged: %d entries", len(c.identities.entries))
	}
}
