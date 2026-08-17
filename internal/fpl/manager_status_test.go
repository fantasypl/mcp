package fpl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// managerStatusServer serves canned picks/history JSON for ManagerStatus,
// keyed only on which endpoint is hit — the test cases vary bank,
// event_transfers, and chips within that payload.
func managerStatusServer(t *testing.T, picksJSON, historyJSON string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entry/12345/event/25/picks/", "/entry/12345/event/15/picks/", "/entry/12345/event/20/picks/":
			w.Write([]byte(picksJSON))
		case "/entry/12345/history/":
			w.Write([]byte(historyJSON))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.BaseURL = srv.URL
	return c
}

func bootstrapAtGW(current, next int) *Bootstrap {
	events := make([]Event, 38)
	for i := range events {
		gw := i + 1
		events[i] = Event{ID: gw, IsCurrent: gw == current, IsNext: gw == next}
	}
	return &Bootstrap{Events: events}
}

func picksJSON(eventTransfers, bank int) string {
	return fmt.Sprintf(`{"picks":[],"active_chip":null,"entry_history":{"bank":%d,"event_transfers":%d,"overall_rank":10000,"total_points":1500,"points_on_bench":4}}`, bank, eventTransfers)
}

func historyJSON(chips ...ChipUsage) string {
	b := `{"current":[],"chips":[`
	for i, c := range chips {
		if i > 0 {
			b += ","
		}
		b += fmt.Sprintf(`{"name":%q,"event":%d}`, c.Name, c.Event)
	}
	return b + `]}`
}

// TestManagerStatusChipReset covers the chip half-reset logic: FPL resets
// all chips after GW19, so a chip's gameweek determines whether it counts
// against the current half, not just whether it was ever played.
func TestManagerStatusChipReset(t *testing.T) {
	tests := []struct {
		name          string
		currentGW     int
		chips         []ChipUsage
		wantRemain    []string
		wantNotRemain []string
	}{
		{
			name:       "second half ignores first half chips",
			currentGW:  25,
			chips:      []ChipUsage{{Name: "wildcard", Event: 5}, {Name: "bboost", Event: 5}},
			wantRemain: []string{"3xc", "bboost", "freehit", "wildcard"},
		},
		{
			name:          "second half counts second half chips",
			currentGW:     25,
			chips:         []ChipUsage{{Name: "wildcard", Event: 22}, {Name: "3xc", Event: 24}},
			wantRemain:    []string{"bboost", "freehit"},
			wantNotRemain: []string{"wildcard", "3xc"},
		},
		{
			name:          "first half counts first half chips",
			currentGW:     15,
			chips:         []ChipUsage{{Name: "wildcard", Event: 10}},
			wantNotRemain: []string{"wildcard"},
		},
		{
			name:       "first half ignores second half chips",
			currentGW:  15,
			chips:      []ChipUsage{{Name: "freehit", Event: 25}},
			wantRemain: []string{"freehit"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := managerStatusServer(t, picksJSON(1, 50), historyJSON(tc.chips...))
			b := bootstrapAtGW(tc.currentGW, tc.currentGW+1)
			got, err := c.ManagerStatus(context.Background(), 12345, b)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantRemain {
				if !slices.Contains(got.ChipsRemaining, want) {
					t.Errorf("chips_remaining = %v, want to contain %q", got.ChipsRemaining, want)
				}
			}
			for _, notWant := range tc.wantNotRemain {
				if slices.Contains(got.ChipsRemaining, notWant) {
					t.Errorf("chips_remaining = %v, want NOT to contain %q", got.ChipsRemaining, notWant)
				}
			}
		})
	}
}

// TestManagerStatusFreeTransfers ports the free-transfer calculation cases:
// zero transfers made last gameweek rolls over to 2, any nonzero count (or
// an active wildcard/free hit) gives 1.
func TestManagerStatusFreeTransfers(t *testing.T) {
	tests := []struct {
		name           string
		eventTransfers int
		chips          []ChipUsage
		want           int
	}{
		{name: "zero transfers rolls over", eventTransfers: 0, want: 2},
		{name: "nonzero transfers gives one", eventTransfers: 2, want: 1},
		{name: "wildcard resets to one", eventTransfers: 0, chips: []ChipUsage{{Name: "wildcard", Event: 20}}, want: 1},
		{name: "freehit resets to one", eventTransfers: 0, chips: []ChipUsage{{Name: "freehit", Event: 20}}, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := managerStatusServer(t, picksJSON(tc.eventTransfers, 50), historyJSON(tc.chips...))
			b := bootstrapAtGW(20, 21)
			got, err := c.ManagerStatus(context.Background(), 12345, b)
			if err != nil {
				t.Fatal(err)
			}
			if got.FreeTransfers != tc.want {
				t.Errorf("free_transfers = %d, want %d", got.FreeTransfers, tc.want)
			}
		})
	}
}

func TestManagerStatusBankConvertedToMillions(t *testing.T) {
	c := managerStatusServer(t, picksJSON(1, 53), historyJSON())
	b := bootstrapAtGW(20, 21)
	got, err := c.ManagerStatus(context.Background(), 12345, b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bank != 5.3 {
		t.Errorf("bank = %v, want 5.3", got.Bank)
	}
}

// TestManagerStatusAllChipsUsedGivesEmptySlice guards against a nil slice
// marshaling to JSON null: chips_remaining must stay an empty list, not
// null, even when every chip is used, so callers can always iterate it.
func TestManagerStatusAllChipsUsedGivesEmptySlice(t *testing.T) {
	c := managerStatusServer(t, picksJSON(1, 50), historyJSON(
		ChipUsage{Name: "wildcard", Event: 20}, ChipUsage{Name: "bboost", Event: 20},
		ChipUsage{Name: "freehit", Event: 20}, ChipUsage{Name: "3xc", Event: 20},
	))
	b := bootstrapAtGW(20, 21)
	got, err := c.ManagerStatus(context.Background(), 12345, b)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChipsRemaining == nil {
		t.Fatal("chips_remaining is nil, want non-nil empty slice (marshals to null instead of [])")
	}
	if len(got.ChipsRemaining) != 0 {
		t.Errorf("chips_remaining = %v, want empty", got.ChipsRemaining)
	}
}
