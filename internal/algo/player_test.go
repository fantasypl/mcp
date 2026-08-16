package algo

import (
	"encoding/json"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// newPlayer returns a plausible mid-table midfielder, so unit tests can vary
// one attribute at a time without every score collapsing to zero.
func newPlayer() *fpl.Player {
	return &fpl.Player{
		ID:            1,
		Team:          1,
		ElementType:   3, // MID
		WebName:       "Test",
		Status:        "a",
		TotalPoints:   120,
		Minutes:       2700, // 30 nineties
		Starts:        30,
		Bonus:         15,
		Form:          numOf(5.0),
		PointsPerGame: numOf(4.5),
		EPNext:        numOf(4.0),
		ICTIndex:      numOf(150),

		ExpectedGoals:              numOf(9.0),
		ExpectedAssists:            numOf(6.0),
		DefensiveContributionPer90: numOf(2.5),

		NowCost:           75,
		SelectedByPercent: numOf(12.5),
		DreamteamCount:    3,
	}
}

// numOf builds an fpl.Num, which has no public constructor because it is
// normally produced by unmarshalling.
func numOf(f float64) fpl.Num {
	var n fpl.Num
	b, _ := json.Marshal(f)
	_ = json.Unmarshal(b, &n)
	return n
}

func TestNewPlayerScoresNonZero(t *testing.T) {
	e := NewEngine(nil)
	if got := e.scorePlayer(newPlayer(), []TeamFixture{{FDR: 3, IsHome: false}}); got <= 0 {
		t.Fatalf("baseline player scored %v; unit tests that vary one field would be meaningless", got)
	}
}
