package algo

import (
	"context"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// setPriceMomentum makes the player a likely riser (net > 0) or faller
// (net < 0) in the stub bootstrap, and available so price_predictions
// doesn't skip them as injured.
func setPriceMomentum(t *testing.T, e *Engine, playerID, net int) {
	t.Helper()
	b := e.client.(*StubClient).bootstrap
	for i := range b.Elements {
		if b.Elements[i].ID == playerID {
			b.Elements[i].Status = "a"
			b.Elements[i].TransfersInEvent, b.Elements[i].TransfersOutEvent = 0, 0
			if net > 0 {
				b.Elements[i].TransfersInEvent = net
			} else {
				b.Elements[i].TransfersOutEvent = -net
			}
			return
		}
	}
	t.Fatalf("player %d not in bootstrap", playerID)
}

// Issue #9: a suggestion whose sell or buy candidate is flagged by
// price_predictions says so inline, with no second call, and ranking is left
// exactly as it was.
func TestTransferSuggestionsShowPriceRisk(t *testing.T) {
	e := newEngineWithSquad(t, "midseason")
	ctx := context.Background()

	base, err := e.TransferSuggestions(ctx, syntheticTeamID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	baseSugg := base.(*TransferSuggestionsResult).TransferSuggestions[0]
	outID := baseSugg.TransferOut.ID
	inID := baseSugg.TransferInOptions[0].ID
	if baseSugg.TransferOut.PriceRisk != "" || baseSugg.TransferInOptions[0].PriceRisk != "" {
		t.Fatalf("test setup: unflagged players already carry a price risk")
	}

	setPriceMomentum(t, e, outID, -600_000)
	setPriceMomentum(t, e, inID, 600_000)

	got, err := e.TransferSuggestions(ctx, syntheticTeamID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	sugg := got.(*TransferSuggestionsResult).TransferSuggestions[0]

	if r := got.(*TransferSuggestionsResult); !strings.Contains(r.PriceRiskNote, "not") || !strings.Contains(r.PriceRiskNote, "safe") {
		t.Errorf("PriceRiskNote = %q, want it to say a missing label does not mean safe", r.PriceRiskNote)
	}
	if base.(*TransferSuggestionsResult).PriceRiskNote != "" {
		t.Errorf("PriceRiskNote should be empty when nothing is labelled")
	}
	if sugg.TransferOut.PriceRisk != "likely to fall tonight" {
		t.Errorf("sell candidate PriceRisk = %q, want %q", sugg.TransferOut.PriceRisk, "likely to fall tonight")
	}
	var buy *TransferInOption
	for i := range sugg.TransferInOptions {
		if sugg.TransferInOptions[i].ID == inID {
			buy = &sugg.TransferInOptions[i]
		}
	}
	if buy == nil || buy.PriceRisk != "likely to rise tonight" {
		t.Errorf("buy candidate PriceRisk = %+v, want %q", buy, "likely to rise tonight")
	}

	// Purely informational: same players in the same order.
	if sugg.TransferOut.ID != outID || len(sugg.TransferInOptions) != len(baseSugg.TransferInOptions) {
		t.Fatalf("price risk changed the suggestion: out %d vs %d, %d options vs %d",
			sugg.TransferOut.ID, outID, len(sugg.TransferInOptions), len(baseSugg.TransferInOptions))
	}
	for i := range baseSugg.TransferInOptions {
		if sugg.TransferInOptions[i].ID != baseSugg.TransferInOptions[i].ID {
			t.Errorf("option %d changed from %d to %d: ranking must not depend on price risk",
				i, baseSugg.TransferInOptions[i].ID, sugg.TransferInOptions[i].ID)
		}
	}
}

// Issue #9 for optimal_transfers: a transfer leg flagged by price_predictions
// carries the flag, and nothing else about the plan changes.
func TestOptimalTransfersShowPriceRisk(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	ctx := context.Background()

	base, err := e.OptimalTransfers(ctx, syntheticTeamID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	baseOpt := base.(*OptimalTransfersResult).Options[0]
	if len(baseOpt.TransfersOut) == 0 || len(baseOpt.TransfersIn) == 0 {
		t.Skip("baseline plan makes no transfers; nothing to annotate")
	}
	outID, inID := baseOpt.TransfersOut[0].ID, baseOpt.TransfersIn[0].ID
	if baseOpt.TransfersOut[0].PriceRisk != "" || baseOpt.TransfersIn[0].PriceRisk != "" {
		t.Fatal("test setup: unflagged players already carry a price risk")
	}

	setPriceMomentum(t, e, outID, -600_000)
	setPriceMomentum(t, e, inID, 600_000)

	got, err := e.OptimalTransfers(ctx, syntheticTeamID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	opt := got.(*OptimalTransfersResult).Options[0]

	if opt.TransfersOut[0].ID != outID || opt.TransfersIn[0].ID != inID {
		t.Fatalf("price risk changed which players are transferred")
	}
	if opt.TransfersOut[0].PriceRisk != "likely to fall tonight" {
		t.Errorf("transfer out PriceRisk = %q, want %q", opt.TransfersOut[0].PriceRisk, "likely to fall tonight")
	}
	if opt.TransfersIn[0].PriceRisk != "likely to rise tonight" {
		t.Errorf("transfer in PriceRisk = %q, want %q", opt.TransfersIn[0].PriceRisk, "likely to rise tonight")
	}
	if r := got.(*OptimalTransfersResult); !strings.Contains(r.PriceRiskNote, "safe") {
		t.Errorf("PriceRiskNote = %q, want it to say a missing label does not mean safe", r.PriceRiskNote)
	}
	if base.(*OptimalTransfersResult).PriceRiskNote != "" {
		t.Error("PriceRiskNote should be empty when nothing is labelled")
	}
	if opt.NetProjectedPoints != baseOpt.NetProjectedPoints {
		t.Errorf("net projected points changed from %v to %v", baseOpt.NetProjectedPoints, opt.NetProjectedPoints)
	}
}

// Issue #9 review: the sell that is most urgent is often an injured or
// suspended player, whom price_predictions skips. They are labelled from their
// own net transfers, using the same 50,000 threshold as the hub's
// price_drop_risks.
func TestPriceRiskForInjuredPlayers(t *testing.T) {
	cases := []struct {
		name   string
		player fpl.Player
		flags  map[int]string
		want   string
	}{
		{"flagged by predictions", fpl.Player{ID: 1, Status: "a"}, map[int]string{1: priceRiskFall}, priceRiskFall},
		{"available, not flagged", fpl.Player{ID: 2, Status: "a", TransfersOutEvent: 900_000}, nil, ""},
		{"suspended, heavily sold", fpl.Player{ID: 3, Status: "s", TransfersOutEvent: 80_000}, nil, priceRiskFall},
		{"injured, heavily bought", fpl.Player{ID: 4, Status: "i", TransfersInEvent: 80_000}, nil, priceRiskRise},
		{"injured, below threshold", fpl.Player{ID: 5, Status: "i", TransfersOutEvent: 49_999}, nil, ""},
		{"injured, at threshold", fpl.Player{ID: 6, Status: "i", TransfersOutEvent: 50_000}, nil, priceRiskFall},
		{"doubtful, heavily sold", fpl.Player{ID: 7, Status: "d", TransfersOutEvent: 80_000}, nil, priceRiskFall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := priceRiskFor(&tc.player, tc.flags); got != tc.want {
				t.Errorf("priceRiskFor = %q, want %q", got, tc.want)
			}
		})
	}
}
