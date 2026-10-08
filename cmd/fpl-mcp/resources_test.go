package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fpl://status reports the next deadline and time remaining from
// fpl.StatusAt, the helper fpl_manager_hub also uses (#19).
func TestStatusResource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bootstrap-static/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "../../testdata/bootstrap_midseason.json")
	}))
	defer srv.Close()

	client := fpl.NewClient()
	client.BaseURL = srv.URL
	// GW1's deadline in the fixture is 2026-08-21T17:30:00Z.
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	ctx := context.Background()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	addResources(s, client, func() time.Time { return now })

	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := s.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	clientSession, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	res, err := clientSession.ReadResource(ctx, &mcp.ReadResourceParams{URI: "fpl://status"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	var got fpl.GameweekStatus
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	b, err := client.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := fpl.StatusAt(b, now); got != want {
		t.Errorf("fpl://status =\n%+v\nwant\n%+v", got, want)
	}
	if got.NextDeadline != "2026-08-21T17:30:00Z" || got.TimeToDeadline != "2d 5h" {
		t.Errorf("deadline = %q / %q, want 2026-08-21T17:30:00Z / 2d 5h", got.NextDeadline, got.TimeToDeadline)
	}
}
