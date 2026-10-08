package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// validTeam / validLeague / validGW / errResult — the input-validation
// helpers every tool handler calls before touching the FPL client.
// ---------------------------------------------------------------------------

func TestValidTeam(t *testing.T) {
	for _, id := range []int{1, 100, 1_000_000, 20_000_000} {
		if e := validTeam(id); e != "" {
			t.Errorf("validTeam(%d) = %q, want valid", id, e)
		}
	}
	for _, id := range []int{0, -1, -999, 20_000_001, 99_999_999} {
		if e := validTeam(id); e == "" {
			t.Errorf("validTeam(%d) = \"\", want an error", id)
		}
	}
}

func TestValidGW(t *testing.T) {
	if e := validGW(nil); e != "" {
		t.Errorf("validGW(nil) = %q, want valid (nil means unspecified)", e)
	}
	for _, gw := range []int{1, 19, 38} {
		gw := gw
		if e := validGW(&gw); e != "" {
			t.Errorf("validGW(%d) = %q, want valid", gw, e)
		}
	}
	for _, gw := range []int{0, -1, 39, 100} {
		gw := gw
		if e := validGW(&gw); e == "" {
			t.Errorf("validGW(%d) = \"\", want an error", gw)
		}
	}
}

func TestErrResultShape(t *testing.T) {
	r := errResult("something broke")
	if r["isError"] != true {
		t.Errorf("isError = %v, want true", r["isError"])
	}
	if r["error"] != "something broke" {
		t.Errorf("error = %v, want %q", r["error"], "something broke")
	}
	if len(r) != 2 {
		t.Errorf("errResult has %d keys, want exactly 2 (isError, error): %v", len(r), r)
	}
}

// ---------------------------------------------------------------------------
// Startup smoke test — the check that would have caught the schema-tag
// panic: build the real server via newServer and drive it over an
// in-memory transport exactly as a client would, with no network access
// (validation failures never reach the fpl.Client).
// ---------------------------------------------------------------------------

func TestServerStartsAndListsEverything(t *testing.T) {
	ctx := context.Background()
	s := newServer(fpl.NewClient())

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

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 16 {
		names := make([]string, len(tools.Tools))
		for i, tl := range tools.Tools {
			names[i] = tl.Name
		}
		t.Errorf("got %d tools, want 16: %v", len(tools.Tools), names)
	}

	resources, err := clientSession.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	if len(resources.Resources) != 2 {
		t.Errorf("got %d resources, want 2", len(resources.Resources))
	}

	prompts, err := clientSession.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("prompts/list: %v", err)
	}
	if len(prompts.Prompts) != 5 {
		t.Errorf("got %d prompts, want 5", len(prompts.Prompts))
	}

	// Clients show title in pickers and argument forms (#27).
	for _, r := range resources.Resources {
		if r.Title == "" {
			t.Errorf("resource %s has no title", r.URI)
		}
	}
	for _, p := range prompts.Prompts {
		if p.Title == "" {
			t.Errorf("prompt %s has no title", p.Name)
		}
		for _, a := range p.Arguments {
			if a.Title == "" || a.Description == "" {
				t.Errorf("prompt %s argument %s needs a title and description", p.Name, a.Name)
			}
		}
	}
}

// mcpb/manifest.json declares every tool and prompt so Claude Desktop, which
// may check runtime prompts against the manifest (#27), sees the same set the
// server registers. A manifest prompt's text must match what prompts/get
// renders when each argument is the manifest's ${arguments.name} placeholder.
func TestManifestMatchesServer(t *testing.T) {
	raw, err := os.ReadFile("../../mcpb/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Prompts []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Arguments   []string `json:"arguments"`
			Text        string   `json:"text"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := newServer(fpl.NewClient()).Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var serverTools, manifestTools []string
	for _, tl := range tools.Tools {
		serverTools = append(serverTools, tl.Name)
	}
	for _, tl := range manifest.Tools {
		manifestTools = append(manifestTools, tl.Name)
	}
	sort.Strings(serverTools)
	sort.Strings(manifestTools)
	if !slices.Equal(serverTools, manifestTools) {
		t.Errorf("manifest tools %v, server tools %v", manifestTools, serverTools)
	}

	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Prompts) != len(prompts.Prompts) {
		t.Fatalf("manifest declares %d prompts, server registers %d", len(manifest.Prompts), len(prompts.Prompts))
	}
	declared := map[string]int{}
	for i, p := range manifest.Prompts {
		declared[p.Name] = i
	}
	for _, p := range prompts.Prompts {
		i, ok := declared[p.Name]
		if !ok {
			t.Errorf("prompt %s is not declared in the manifest", p.Name)
			continue
		}
		mp := manifest.Prompts[i]
		if mp.Description != p.Description {
			t.Errorf("prompt %s description: manifest %q, server %q", p.Name, mp.Description, p.Description)
		}
		args := map[string]string{}
		var names []string
		for _, a := range p.Arguments {
			args[a.Name] = "${arguments." + a.Name + "}"
			names = append(names, a.Name)
		}
		if !slices.Equal(names, mp.Arguments) {
			t.Errorf("prompt %s arguments: manifest %v, server %v", p.Name, mp.Arguments, names)
		}
		res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: p.Name, Arguments: args})
		if err != nil {
			t.Fatalf("prompts/get %s: %v", p.Name, err)
		}
		if got := res.Messages[0].Content.(*mcp.TextContent).Text; got != mp.Text {
			t.Errorf("prompt %s text differs from the manifest:\nserver:   %q\nmanifest: %q", p.Name, got, mp.Text)
		}
	}
}

// callTool drives a real tools/call over an in-memory transport and decodes
// the structured result — the same JSON body a client inspects for
// isError, not the MCP protocol-level error.
func callTool(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	ctx := context.Background()
	s := newServer(fpl.NewClient())

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

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s: structured content is %T, want map[string]any: %v", name, res.StructuredContent, res.StructuredContent)
	}
	return out
}

func isError(m map[string]any) bool {
	v, _ := m["isError"].(bool)
	return v
}

// ---------------------------------------------------------------------------
// Per-tool validation. Every case here must be rejected before any
// fpl.Client call, so a live fpl.NewClient() in callTool never actually
// reaches the network.
// ---------------------------------------------------------------------------

func TestToolValidation(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
	}{
		{"captain_pick", map[string]any{"gameweek": 99}},
		{"captain_pick", map[string]any{"gameweek": 0}},
		{"captain_pick", map[string]any{"gameweek": -1}},

		{"differential_finder", map[string]any{"max_ownership_pct": -5}},
		{"differential_finder", map[string]any{"max_ownership_pct": 0.0}},
		{"differential_finder", map[string]any{"max_ownership_pct": 101}},
		{"differential_finder", map[string]any{"gameweek": 39}},

		{"fixture_outlook", map[string]any{"position": "INVALID"}},
		{"fixture_outlook", map[string]any{"position": "STR"}},

		{"player_comparison", map[string]any{"player_names": []string{"only_one"}}},
		{"player_comparison", map[string]any{"player_names": []string{}}},
		{"player_comparison", map[string]any{"player_names": []string{"a", "b", "c", "d", "e"}}},

		{"live_points", map[string]any{"team_id": -1}},
		{"live_points", map[string]any{"team_id": 0}},
		{"live_points", map[string]any{"team_id": 99_999_999}},

		{"is_hit_worth_it", map[string]any{"player_out_id": 5, "player_in_id": 5}},
		{"is_hit_worth_it", map[string]any{"player_out_id": -1, "player_in_id": 10}},
		{"is_hit_worth_it", map[string]any{"player_out_id": 10, "player_in_id": -1}},
		{"is_hit_worth_it", map[string]any{"player_out_id": 0, "player_in_id": 10}},
		{"is_hit_worth_it", map[string]any{"player_out_id": 10, "player_in_id": 11, "team_id": -1}},
		{"is_hit_worth_it", map[string]any{"player_out_id": 10, "player_in_id": 11, "team_id": 99_999_999}},

		{"manager_transfer_history", map[string]any{"team_id": 0}},
		{"manager_transfer_history", map[string]any{"team_id": -1}},
		{"manager_transfer_history", map[string]any{"team_id": 99_999_999}},

		{"fpl_manager_hub", map[string]any{"team_id": 0}},
		{"fpl_manager_hub", map[string]any{"team_id": -1}},
		{"fpl_manager_hub", map[string]any{"team_id": 99_999_999}},

		{"chip_strategy", map[string]any{"team_id": -1}},
		{"chip_strategy", map[string]any{"team_id": 0}},

		{"squad_scout", map[string]any{"team_id": 0}},
		{"squad_scout", map[string]any{"team_id": -1}},
		{"squad_scout", map[string]any{"team_id": 99_999_999}},

		{"transfer_suggestions", map[string]any{"team_id": 0}},
		{"transfer_suggestions", map[string]any{"team_id": -1}},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			got := callTool(t, tc.tool, tc.args)
			if !isError(got) {
				t.Errorf("%s%v = %v, want isError true", tc.tool, tc.args, got)
			}
		})
	}
}
