package fplauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLogin(t *testing.T) {
	good := `{"authority":"https://account.premierleague.com/as/","client_id":"abc","refresh_token":"rt-1"}`
	l, err := ParseLogin(good)
	if err != nil {
		t.Fatalf("ParseLogin: %v", err)
	}
	if l.Authority != "https://account.premierleague.com/as" || l.ClientID != "abc" || l.RefreshToken != "rt-1" {
		t.Errorf("got %+v", l)
	}

	for name, raw := range map[string]string{
		"not json":       "rt-1",
		"missing client": `{"authority":"https://account.premierleague.com/as","refresh_token":"rt-1"}`,
		"missing token":  `{"authority":"https://account.premierleague.com/as","client_id":"abc"}`,
		"http":           `{"authority":"http://account.premierleague.com/as","client_id":"abc","refresh_token":"rt-1"}`,
		"other host":     `{"authority":"https://evil.example/as","client_id":"abc","refresh_token":"rt-1"}`,
		"lookalike host": `{"authority":"https://notpremierleague.com/as","client_id":"abc","refresh_token":"rt-1"}`,
		"userinfo":       `{"authority":"https://x@account.premierleague.com/as","client_id":"abc","refresh_token":"rt-1"}`,
	} {
		_, err := ParseLogin(raw)
		var ae *Error
		if !errors.As(err, &ae) {
			t.Errorf("%s: got %v, want *Error", name, err)
			continue
		}
		if strings.Contains(err.Error(), "rt-1") {
			t.Errorf("%s: error leaks the token: %v", name, err)
		}
	}
}

func TestFromEnvUnset(t *testing.T) {
	t.Setenv(EnvLogin, "")
	s, err := FromEnv(t.TempDir())
	if s != nil || err != nil {
		t.Errorf("FromEnv with no login = %v, %v; want nil, nil", s, err)
	}
}

// provider is a fake OIDC provider that rotates refresh tokens like the real
// one: each token works once and is replaced by the next.
type provider struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	valid  map[string]bool
	next   int
	issuer string // overrides the discovered issuer when set
	calls  int
	status int // forces a token endpoint status when set
}

func newProvider(t *testing.T, valid ...string) *provider {
	p := &provider{t: t, valid: map[string]bool{}}
	for _, v := range valid {
		p.valid[v] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/as/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss := p.srv.URL + "/as"
		if p.issuer != "" {
			iss = p.issuer
		}
		json.NewEncoder(w).Encode(map[string]string{"issuer": iss, "token_endpoint": p.srv.URL + "/as/token"})
	})
	mux.HandleFunc("/as/token", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.calls++
		if p.status != 0 {
			w.WriteHeader(p.status)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" || r.FormValue("client_id") != "client-1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rt := r.FormValue("refresh_token")
		if !p.valid[rt] {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		delete(p.valid, rt)
		p.next++
		nt := fmt.Sprintf("rt-rotated-%d", p.next)
		p.valid[nt] = true
		json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("at-%d", p.next), "refresh_token": nt, "expires_in": 3600})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) source(dir, refresh string) *Source {
	s := New(Login{Authority: p.srv.URL + "/as", ClientID: "client-1", RefreshToken: refresh}, dir)
	s.checkHost = func(string) error { return nil }
	return s
}

func TestRefreshRotatesAndPersists(t *testing.T) {
	p := newProvider(t, "rt-pasted")
	dir := filepath.Join(t.TempDir(), "auth")
	s := p.source(dir, "rt-pasted")

	at, err := s.AccessToken(context.Background())
	if err != nil || at != "at-1" {
		t.Fatalf("AccessToken = %q, %v; want at-1", at, err)
	}
	// Cached until near expiry: no second exchange.
	if at, _ := s.AccessToken(context.Background()); at != "at-1" || p.calls != 1 {
		t.Errorf("second call = %q after %d exchanges; want cached at-1 after 1", at, p.calls)
	}

	info, err := os.Stat(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600", info.Mode().Perm())
	}

	// A restart with the same pasted token uses the saved rotated token, since
	// the pasted one has been spent.
	s2 := p.source(dir, "rt-pasted")
	if at, err := s2.AccessToken(context.Background()); err != nil || at != "at-2" {
		t.Errorf("after restart AccessToken = %q, %v; want at-2", at, err)
	}
}

func TestNewPasteReplacesSavedChain(t *testing.T) {
	p := newProvider(t, "rt-old")
	dir := t.TempDir()
	if _, err := p.source(dir, "rt-old").AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.valid["rt-new"] = true
	p.mu.Unlock()
	s := p.source(dir, "rt-new")
	if s.refreshToken != "rt-new" {
		t.Errorf("refresh token = %q, want the newly pasted rt-new", s.refreshToken)
	}
	if _, err := s.AccessToken(context.Background()); err != nil {
		t.Errorf("AccessToken with new paste: %v", err)
	}
}

func TestExpiredLoginIsClearAndSecret(t *testing.T) {
	p := newProvider(t) // nothing valid
	s := p.source(t.TempDir(), "rt-dead-secret")
	_, err := s.AccessToken(context.Background())
	var ae *Error
	if !errors.As(err, &ae) || ae.Msg != Expired {
		t.Fatalf("got %v, want Expired", err)
	}
	if strings.Contains(err.Error(), "rt-dead-secret") {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestOutageIsNotReportedAsExpired(t *testing.T) {
	p := newProvider(t, "rt-1")
	p.status = http.StatusServiceUnavailable
	_, err := p.source("", "rt-1").AccessToken(context.Background())
	if err == nil || strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "503") {
		t.Errorf("got %v, want a 503 error that does not claim expiry", err)
	}
}

func TestRetriesWithTokenRotatedByAnotherProcess(t *testing.T) {
	p := newProvider(t, "rt-pasted")
	dir := t.TempDir()
	a := p.source(dir, "rt-pasted")
	b := p.source(dir, "rt-pasted")
	if _, err := a.AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	// b still holds the spent pasted token; it should pick up a's rotation.
	if _, err := b.AccessToken(context.Background()); err != nil {
		t.Errorf("second process: %v", err)
	}
}

func TestInvalidateForcesRefresh(t *testing.T) {
	p := newProvider(t, "rt-1")
	s := p.source("", "rt-1")
	s.AccessToken(context.Background())
	s.Invalidate()
	if at, err := s.AccessToken(context.Background()); err != nil || at != "at-2" {
		t.Errorf("after Invalidate = %q, %v; want at-2", at, err)
	}
}

func TestRefreshesNearExpiry(t *testing.T) {
	p := newProvider(t, "rt-1")
	s := p.source("", "rt-1")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.AccessToken(context.Background())
	now = now.Add(time.Hour - 30*time.Second)
	if at, _ := s.AccessToken(context.Background()); at != "at-2" {
		t.Errorf("30s before expiry got %q, want a refreshed at-2", at)
	}
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	p := newProvider(t, "rt-1")
	p.issuer = "https://elsewhere.premierleague.com/as"
	_, err := p.source("", "rt-1").AccessToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Errorf("got %v, want an issuer mismatch error", err)
	}
	if p.calls != 0 {
		t.Errorf("token endpoint called %d times after a bad discovery, want 0", p.calls)
	}
}

func TestDiscoveryRejectsForeignTokenEndpoint(t *testing.T) {
	p := newProvider(t, "rt-1")
	s := New(Login{Authority: p.srv.URL + "/as", ClientID: "client-1", RefreshToken: "rt-1"}, "")
	// Keep the real host check: the httptest endpoint is not premierleague.com.
	if _, err := s.AccessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "outside premierleague.com") {
		t.Errorf("got %v, want the token endpoint rejected", err)
	}
	if p.calls != 0 {
		t.Errorf("token endpoint called %d times, want 0", p.calls)
	}
}
