// Package fplauth supplies access tokens for FPL's logged-in endpoints, such
// as /my-team/{id}/. It is off unless the user sets FPL_MCP_LOGIN.
//
// FPL signs users in through an OpenID Connect provider. The fantasy site
// keeps the session in the browser's local storage under the key
// "oidc.user:<authority>:<client_id>", and the value holds a refresh token.
// The user copies the authority, the client ID and the refresh token from
// there (README.md gives a one-line console snippet). Nothing about the
// provider is built in: the token endpoint comes from the authority's OpenID
// discovery document.
//
// The provider rotates the refresh token on every use, so the old one stops
// working once exchanged. The current one is kept in a state file (mode 0600)
// in the user's cache directory. The state file records a hash of the
// refresh token the user pasted, so pasting a new one replaces the saved
// chain rather than being ignored.
//
// No token is ever logged, put in an error message or returned in tool
// output.
package fplauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EnvLogin is the environment variable holding the pasted login JSON.
const EnvLogin = "FPL_MCP_LOGIN"

const (
	// expiryMargin refreshes an access token this long before it expires,
	// so a token never lapses between the check and the request.
	expiryMargin = time.Minute
	// defaultExpiry applies when the token response has no expires_in.
	defaultExpiry = 5 * time.Minute
	httpTimeout   = 15 * time.Second
	stateFileName = "session.json"
)

// Login is what the user pastes: where to refresh, as which client, and the
// refresh token itself.
type Login struct {
	Authority    string `json:"authority"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
}

// ErrNotConfigured means FPL_MCP_LOGIN is unset.
var ErrNotConfigured = errors.New("FPL login is not set up. It is optional and off by default: set fpl_login in the extension settings (or FPL_MCP_LOGIN) as described in the README")

// Error is a login failure with a message safe to show the user. It never
// contains a token.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Expired is the message for a refresh token the provider refuses.
const Expired = "FPL login was refused: the saved login has expired or been signed out. Log in at fantasy.premierleague.com, copy a fresh login with the README's console snippet, and paste it into fpl_login (or FPL_MCP_LOGIN)"

// ParseLogin validates the pasted JSON. The authority must be an https URL
// on premierleague.com or one of its subdomains, so a mistyped or tampered
// paste can never send the refresh token anywhere else.
func ParseLogin(raw string) (Login, error) {
	var l Login
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &l); err != nil {
		return Login{}, &Error{Msg: "fpl_login is not valid JSON. Paste exactly what the README's console snippet copies"}
	}
	l.Authority = strings.TrimRight(strings.TrimSpace(l.Authority), "/")
	l.ClientID = strings.TrimSpace(l.ClientID)
	l.RefreshToken = strings.TrimSpace(l.RefreshToken)
	if l.Authority == "" || l.ClientID == "" || l.RefreshToken == "" {
		return Login{}, &Error{Msg: "fpl_login needs authority, client_id and refresh_token. Paste exactly what the README's console snippet copies"}
	}
	if err := checkHost(l.Authority); err != nil {
		return Login{}, err
	}
	return l, nil
}

// checkHost accepts only https URLs on premierleague.com.
func checkHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return &Error{Msg: "fpl_login authority must be an https URL on premierleague.com"}
	}
	h := strings.ToLower(u.Hostname())
	if h != "premierleague.com" && !strings.HasSuffix(h, ".premierleague.com") {
		return &Error{Msg: "fpl_login authority must be an https URL on premierleague.com"}
	}
	return nil
}

// Source hands out access tokens, refreshing and rotating as needed. It is
// safe for concurrent use.
type Source struct {
	login     Login
	stateFile string // "" keeps the rotated token in memory only
	HTTP      *http.Client
	now       func() time.Time
	// checkHost vets the discovered token endpoint. Tests swap it to allow
	// an httptest server.
	checkHost func(string) error

	mu            sync.Mutex
	refreshToken  string
	tokenEndpoint string
	accessToken   string
	expires       time.Time
}

type state struct {
	// Seed is the SHA-256 of the refresh token the user pasted. When the
	// pasted token changes, the saved chain belongs to an old login.
	Seed         string `json:"seed_sha256"`
	RefreshToken string `json:"refresh_token"`
}

// FromEnv returns a Source when FPL_MCP_LOGIN is set, nil when it is unset,
// and an error when it is set but malformed. stateDir may be "".
func FromEnv(stateDir string) (*Source, error) {
	raw := strings.TrimSpace(os.Getenv(EnvLogin))
	if raw == "" {
		return nil, nil
	}
	l, err := ParseLogin(raw)
	if err != nil {
		return nil, err
	}
	return New(l, stateDir), nil
}

// New returns a Source for l. stateDir holds the rotated refresh token; ""
// keeps it in memory only, so it is lost on restart.
func New(l Login, stateDir string) *Source {
	s := &Source{
		login:        l,
		HTTP:         &http.Client{Timeout: httpTimeout, CheckRedirect: noRedirect},
		now:          time.Now,
		checkHost:    checkHost,
		refreshToken: l.RefreshToken,
	}
	if stateDir != "" {
		s.stateFile = filepath.Join(stateDir, stateFileName)
		if st, ok := s.readState(); ok && st.Seed == seedOf(l.RefreshToken) && st.RefreshToken != "" {
			s.refreshToken = st.RefreshToken
		}
	}
	return s
}

// noRedirect stops the client following a redirect, which could carry the
// refresh token to another host.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func seedOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// AccessToken returns a valid access token, refreshing it if it is missing
// or about to expire.
func (s *Source) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accessToken != "" && s.now().Before(s.expires.Add(-expiryMargin)) {
		return s.accessToken, nil
	}
	if err := s.refresh(ctx); err != nil {
		return "", err
	}
	return s.accessToken, nil
}

// Invalidate drops the cached access token, for when the API rejects it
// before its stated expiry.
func (s *Source) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessToken = ""
}

// refresh exchanges the refresh token. Callers hold s.mu.
func (s *Source) refresh(ctx context.Context) error {
	if s.tokenEndpoint == "" {
		ep, err := s.discover(ctx)
		if err != nil {
			return err
		}
		s.tokenEndpoint = ep
	}

	tok, status, err := s.exchange(ctx, s.refreshToken)
	if err != nil {
		return err
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		// Another fpl-mcp process sharing the state file may have used and
		// rotated our token. Its replacement is on disk; try that once.
		if st, ok := s.readState(); ok && st.Seed == seedOf(s.login.RefreshToken) && st.RefreshToken != "" && st.RefreshToken != s.refreshToken {
			s.refreshToken = st.RefreshToken
			tok, status, err = s.exchange(ctx, s.refreshToken)
			if err != nil {
				return err
			}
		}
	}
	switch {
	case status == http.StatusBadRequest || status == http.StatusUnauthorized:
		s.accessToken = ""
		return &Error{Msg: Expired}
	case status < 200 || status >= 300:
		return &Error{Msg: fmt.Sprintf("FPL login service returned %d. Try again later", status)}
	case tok.AccessToken == "":
		return &Error{Msg: "FPL login service returned no access token. Try again later"}
	}

	expiresIn := time.Duration(tok.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = defaultExpiry
	}
	s.accessToken = tok.AccessToken
	s.expires = s.now().Add(expiresIn)
	if tok.RefreshToken != "" && tok.RefreshToken != s.refreshToken {
		s.refreshToken = tok.RefreshToken
		s.writeState()
	}
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchange posts a refresh_token grant. A non-2xx status is returned, not
// treated as an error, so the caller can tell a refused token from an outage.
func (s *Source) exchange(ctx context.Context, refreshToken string) (tokenResponse, int, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {s.login.ClientID},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, 0, &Error{Msg: "could not build the FPL login request"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		// The transport error names the URL, never the form body.
		return tokenResponse{}, 0, &Error{Msg: "could not reach the FPL login service: " + scrub(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return tokenResponse{}, resp.StatusCode, nil
	}
	var tok tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return tokenResponse{}, 0, &Error{Msg: "FPL login service sent a response that is not JSON"}
	}
	return tok, resp.StatusCode, nil
}

// discover reads the authority's OpenID discovery document for the token
// endpoint, and checks the issuer and endpoint stay on premierleague.com.
func (s *Source) discover(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.login.Authority+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", &Error{Msg: "could not build the FPL login discovery request"}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", &Error{Msg: "could not reach the FPL login service: " + scrub(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", &Error{Msg: fmt.Sprintf("FPL login discovery returned %d. Check the authority in fpl_login", resp.StatusCode)}
	}
	var doc struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", &Error{Msg: "FPL login discovery sent a response that is not JSON"}
	}
	if strings.TrimRight(doc.Issuer, "/") != s.login.Authority {
		return "", &Error{Msg: "FPL login discovery names a different issuer than the authority in fpl_login"}
	}
	if err := s.checkHost(doc.TokenEndpoint); err != nil {
		return "", &Error{Msg: "FPL login discovery gave a token endpoint outside premierleague.com"}
	}
	return doc.TokenEndpoint, nil
}

// scrub reduces a transport error to its cause. A *url.Error carries the
// request URL, which for these requests holds no secret, but keeping only
// the cause keeps messages short.
func scrub(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

func (s *Source) readState() (state, bool) {
	if s.stateFile == "" {
		return state{}, false
	}
	b, err := os.ReadFile(s.stateFile)
	if err != nil {
		return state{}, false
	}
	var st state
	if json.Unmarshal(b, &st) != nil {
		return state{}, false
	}
	return st, true
}

// writeState saves the current refresh token atomically, readable only by
// the user. A failure is ignored: the token still works for this process,
// and the next start falls back to the pasted token.
func (s *Source) writeState() {
	if s.stateFile == "" {
		return
	}
	dir := filepath.Dir(s.stateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	b, err := json.Marshal(state{Seed: seedOf(s.login.RefreshToken), RefreshToken: s.refreshToken})
	if err != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return
	}
	tmp := f.Name()
	// After a successful rename the temp file is gone, so this only cleans
	// up after a failure.
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp, s.stateFile)
}
