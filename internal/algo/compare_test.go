package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

// newCompareEngine mirrors newEngine but additionally serves the two
// player-summary fixtures compare.go's home/away form split needs — synthetic
// match histories, not real ones; see testdata/player_summary_{411,426}.json.
func newCompareEngine(t *testing.T, fixture string) *Engine {
	t.Helper()
	c := &stubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_"+fixture+".json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
		playerSummaries: map[int]*fpl.PlayerSummary{
			411: loadJSON[*fpl.PlayerSummary](t, testdataPath("player_summary_411.json")),
			426: loadJSON[*fpl.PlayerSummary](t, testdataPath("player_summary_426.json")),
		},
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func TestComparePlayersMatchesGolden(t *testing.T) {
	for _, tc := range []struct{ fixture, suffix string }{
		{"preseason", ""},
		{"midseason", "_mid"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			e := newCompareEngine(t, tc.fixture)

			cases := []struct {
				name           string
				playerNames    []string
				gameweeksAhead int
				golden         string
			}{
				{"success", []string{"Haaland", "B.Fernandes"}, 4, "compare_haaland_fernandes"},
				{"not enough names", []string{"Haaland"}, 4, "compare_not_enough_names"},
				{"no match", []string{"Haaland", "Nonexistentplayerxyz"}, 4, "compare_no_match"},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					got, err := e.ComparePlayers(context.Background(), c.playerNames, c.gameweeksAhead)
					if err != nil {
						t.Fatal(err)
					}
					golden.Assert(t, goldenPath(c.golden+tc.suffix), got)
				})
			}
		})
	}
}

func TestComparePlayersTooManyNames(t *testing.T) {
	e := newCompareEngine(t, "preseason")
	got, err := e.ComparePlayers(context.Background(), []string{"a", "b", "c", "d", "e"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != "Please provide at most 4 player names to compare." {
		t.Errorf("Error = %q", got.Error)
	}
}
