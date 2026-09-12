package openfootball

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sampleCLText is a trimmed excerpt of the real 2025-26 UEFA Champions
// League season file fetched live from openfootball/champions-league
// during development — verbatim, not hand-constructed, so the parser is
// tested against the actual grammar rather than an idealized version of it.
// It spans Matchday 1 (tests a multi-match same-kickoff-time block, and a
// match with no half-time score in parentheses) and the Matchday 6/7
// boundary (tests the year carrying forward across Dec 2025 dates with no
// year printed, then updating from an explicit "Jan 20 2026").
const sampleCLText = `= UEFA Champions League 2025/26

# Date       Tue Sep 16 2025 - Sat May 30 2026 (256d)
# Teams      36
# Matches    189
# Stages     League (144)  Playoffs (16)  Finals (29)



▪ League, Matchday 1
  Tue Sep 16 2025
    18:45  Athletic Club (ESP)     v Arsenal FC (ENG)         0-2 (0-0)
           PSV (NED)               v Royale Union Saint-Gilloise (BEL)  1-3 (0-2)
    21:00  Juventus FC (ITA)       v Borussia Dortmund (GER)  4-4 (0-0)
           Tottenham Hotspur FC (ENG) v Villarreal CF (ESP)      1-0 (1-0)


▪ League, Matchday 6
  Tue Dec 9
    16:30  FK Kairat (KAZ)         v PAE Olympiakos SFP (GRE)  0-1 (0-0)
    21:00  Tottenham Hotspur FC (ENG) v SK Slavia Praha (CZE)    3-0 (1-0)
  Wed Dec 10
    18:45  Villarreal CF (ESP)     v FC København (DEN)       2-3 (0-1)
    21:00  Athletic Club (ESP)     v Paris Saint-Germain FC (FRA)  0-0
           Club Brugge KV (BEL)    v Arsenal FC (ENG)         0-3 (0-1)


▪ League, Matchday 7
  Tue Jan 20 2026
    16:30  FK Kairat (KAZ)         v Club Brugge KV (BEL)     1-4 (0-2)
    21:00  FC Internazionale Milano (ITA) v Arsenal FC (ENG)         1-3 (1-2)
`

func TestParseMatches(t *testing.T) {
	matches, err := parseMatches([]byte(sampleCLText))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 11 {
		t.Fatalf("got %d matches, want 11", len(matches))
	}

	want := func(i int, home, away string, kickoff time.Time) {
		t.Helper()
		if matches[i].Home != home || matches[i].Away != away {
			t.Errorf("match %d = %q v %q, want %q v %q", i, matches[i].Home, matches[i].Away, home, away)
		}
		if !matches[i].Kickoff.Equal(kickoff) {
			t.Errorf("match %d kickoff = %v, want %v", i, matches[i].Kickoff, kickoff)
		}
	}

	// Matchday 1: the second match at 18:45 carries the time forward from
	// the first (no leading HH:MM on its own line).
	want(0, "Athletic Club (ESP)", "Arsenal FC (ENG)", time.Date(2025, 9, 16, 18, 45, 0, 0, time.UTC))
	want(1, "PSV (NED)", "Royale Union Saint-Gilloise (BEL)", time.Date(2025, 9, 16, 18, 45, 0, 0, time.UTC))
	want(2, "Juventus FC (ITA)", "Borussia Dortmund (GER)", time.Date(2025, 9, 16, 21, 0, 0, 0, time.UTC))

	// Matchday 6, Dec 9/10: no year printed anywhere in this block — must
	// carry forward 2025 from the last explicit year (Sep 16 2025).
	want(4, "FK Kairat (KAZ)", "PAE Olympiakos SFP (GRE)", time.Date(2025, 12, 9, 16, 30, 0, 0, time.UTC))
	want(5, "Tottenham Hotspur FC (ENG)", "SK Slavia Praha (CZE)", time.Date(2025, 12, 9, 21, 0, 0, 0, time.UTC))

	// A match with no half-time score at all ("0-0", not "0-0 (0-0)") must
	// still parse — the parser never looks past the team names anyway.
	want(7, "Athletic Club (ESP)", "Paris Saint-Germain FC (FRA)", time.Date(2025, 12, 10, 21, 0, 0, 0, time.UTC))

	// Matchday 7: "Jan 20 2026" supplies an explicit year that must
	// override the carried-forward 2025, not be ignored.
	want(9, "FK Kairat (KAZ)", "Club Brugge KV (BEL)", time.Date(2026, 1, 20, 16, 30, 0, 0, time.UTC))
	want(10, "FC Internazionale Milano (ITA)", "Arsenal FC (ENG)", time.Date(2026, 1, 20, 21, 0, 0, 0, time.UTC))
}

func TestFPLTeamFor(t *testing.T) {
	tests := []struct {
		clubName string
		want     string
		ok       bool
	}{
		{"Arsenal FC (ENG)", "ARS", true},
		{"Arsenal FC", "ARS", true},
		{"Tottenham Hotspur FC (ENG)", "TOT", true},
		{"Real Madrid CF (ESP)", "", false},
		{"FC Barcelona (ESP)", "", false},
	}
	for _, tt := range tests {
		got, ok := FPLTeamFor(tt.clubName)
		if got != tt.want || ok != tt.ok {
			t.Errorf("FPLTeamFor(%q) = (%q, %v), want (%q, %v)", tt.clubName, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFetchCachesAndReportsNotAvailable(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/2025-26/cl.txt":
			_, _ = w.Write([]byte(sampleCLText))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(t.TempDir())
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	matches, err := c.Matches(context.Background(), "2025-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 11 {
		t.Fatalf("got %d matches, want 11", len(matches))
	}
	if hits != 1 {
		t.Fatalf("expected 1 network hit, got %d", hits)
	}

	// Second call within TTL should hit the disk cache, not the network.
	if _, err := c.Matches(context.Background(), "2025-26"); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("expected the second call to use the cache, got %d network hits", hits)
	}

	// A season with no upstream file yet must report ErrNotAvailable, not
	// a generic error.
	if _, err := c.Matches(context.Background(), "2026-27"); err != ErrNotAvailable {
		t.Errorf("expected ErrNotAvailable for a missing season, got %v", err)
	}
}
