package footballdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A BOM-prefixed header, as the real files ship, plus one unplayed row and
// one row with missing Avg prices that must fall back to Bet365.
const sample = "\ufeffDiv,Date,HomeTeam,AwayTeam,FTHG,FTAG,Referee,B365H,B365D,B365A,AvgH,AvgD,AvgA,Avg>2.5,Avg<2.5\n" +
	"E0,15/08/2025,Liverpool,Bournemouth,4,2,A Taylor,1.3,6,8.5,1.31,5.96,8.31,1.36,3.2\n" +
	"E0,16/08/2025,Man United,Tottenham,0,0,C Pawson,2.25,3.5,2.9,,,,,\n" +
	"E0,30/08/2025,Arsenal,Wolves,,,,1.2,7,12,1.2,7,12,1.5,2.6\n"

func TestParse(t *testing.T) {
	ms, err := parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d matches, want 3", len(ms))
	}
	if !ms[0].Played || ms[0].HomeGoals != 4 || ms[0].AwayGoals != 2 {
		t.Errorf("row 0 result wrong: %+v", ms[0])
	}
	if ms[0].Prices.Home != 1.31 || ms[0].Prices.Over25 != 1.36 {
		t.Errorf("row 0 should use Avg prices: %+v", ms[0].Prices)
	}
	if ms[1].Home != "Man Utd" || ms[1].Away != "Spurs" {
		t.Errorf("names not canonicalised: %s v %s", ms[1].Home, ms[1].Away)
	}
	if ms[1].Prices.Home != 2.25 {
		t.Errorf("row 1 should fall back to Bet365: %+v", ms[1].Prices)
	}
	if ms[2].Played {
		t.Errorf("row 2 has no score and must be unplayed")
	}
}

func TestParseMissingColumn(t *testing.T) {
	if _, err := parse([]byte("Foo,Bar\n1,2\n")); err == nil {
		t.Fatal("expected error for missing columns")
	}
}

func TestSeasonPath(t *testing.T) {
	if p, err := seasonPath("2025-26"); err != nil || p != "2526" {
		t.Errorf("seasonPath = %q, %v", p, err)
	}
	if _, err := seasonPath("2025"); err == nil {
		t.Error("expected error for malformed season")
	}
}

func TestSeasonCachesToDisk(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/2425/E0.csv" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()

	c := NewClient(t.TempDir())
	c.BaseURL = srv.URL
	c.RetryBackoff = time.Millisecond
	c.now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	for i := 0; i < 2; i++ {
		ms, err := c.Season(context.Background(), "2024-25")
		if err != nil || len(ms) != 3 {
			t.Fatalf("Season: %d matches, err %v", len(ms), err)
		}
	}
	if hits != 1 {
		t.Errorf("finished season fetched %d times, want 1 (cached)", hits)
	}
	if _, err := c.Season(context.Background(), "2019-20"); err == nil {
		t.Error("expected error on 404")
	}
}

func TestDownloadRetriesTransientErrors(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()
	c := NewClient(t.TempDir())
	c.BaseURL, c.RetryBackoff = srv.URL, time.Millisecond
	if _, err := c.Season(context.Background(), "2025-26"); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if hits != 3 {
		t.Errorf("hits = %d, want 3", hits)
	}
}
