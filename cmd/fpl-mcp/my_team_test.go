package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fplauth"
)

// Without a login, my_team says the login is optional and off, and makes no
// request.
func TestMyTeamWithoutLogin(t *testing.T) {
	t.Setenv(fplauth.EnvLogin, "")
	got := callTool(t, "my_team", nil)
	if !isError(got) || !strings.Contains(got["error"].(string), "off by default") {
		t.Errorf("got %v, want an error saying the login is off by default", got)
	}
}

// A malformed login is reported by my_team, not by stopping the server, and
// the message never echoes the token.
func TestMyTeamMalformedLogin(t *testing.T) {
	t.Setenv(fplauth.EnvLogin, `{"authority":"https://evil.example/as","client_id":"c","refresh_token":"secret-rt"}`)
	got := callTool(t, "my_team", nil)
	msg, _ := got["error"].(string)
	if !isError(got) || !strings.Contains(msg, "premierleague.com") {
		t.Errorf("got %v, want an error about the authority host", got)
	}
	if strings.Contains(msg, "secret-rt") {
		t.Errorf("error leaks the token: %q", msg)
	}
}

// The README's example of what to paste must pass the same check the server
// runs, so the docs and the parser cannot drift apart.
func TestReadmeLoginExampleParses(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)<!-- fpl-login-example -->\\s*```json\\s*(.*?)\\s*```").FindSubmatch(raw)
	if m == nil {
		t.Fatal("README has no fpl-login-example block")
	}
	l, err := fplauth.ParseLogin(string(m[1]))
	if err != nil {
		t.Fatalf("README example does not parse: %v", err)
	}
	if l.Authority != "https://account.premierleague.com/as" {
		t.Errorf("authority = %q", l.Authority)
	}
}
