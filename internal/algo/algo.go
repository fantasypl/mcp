// Package algo implements the FPL scoring algorithms: captain picks, chip
// strategy, transfer suggestions, comparisons, and the Engine's other methods.
//
// Two deliberate structural choices:
//
//   - Algorithms are methods on Engine rather than module-level functions, so
//     weights and the FPL client are injected instead of loaded at import time
//     behind a swallowed exception.
//   - Players arrive as typed fpl.Player values rather than dicts.
//
// Where a Go operation behaves differently from the required contract — rounding, capitalisation, sort
// stability — see pyfmt.go and the comments at each call site.
package algo

import (
	"context"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
)

// ptr returns a pointer to v — for constructing *int/*string literals inline,
// which several output shapes need for fields that are genuinely optional
// (nil) rather than merely zero.
func ptr[T any](v T) *T { return &v }

// PositionMap maps element_type to the short position name.
var PositionMap = map[int]string{1: "GKP", 2: "DEF", 3: "MID", 4: "FWD"}

// InjuryStatuses are the status codes meaning a player is unavailable:
// injured, doubtful, suspended, unavailable.
var InjuryStatuses = map[string]bool{"i": true, "d": true, "s": true, "u": true}

// Position returns the short position name, or "?" when the element type is unknown.
func Position(elementType int) string {
	if p, ok := PositionMap[elementType]; ok {
		return p
	}
	return "?"
}

// Client is the subset of *fpl.Client the algorithms need. An interface so
// tests can serve frozen payloads without a live server.
type Client interface {
	Bootstrap(ctx context.Context) (*fpl.Bootstrap, error)
	Fixtures(ctx context.Context) ([]fpl.Fixture, error)
	TeamPicks(ctx context.Context, teamID, gw int) (*fpl.TeamPicks, error)
	LivePoints(ctx context.Context, gw int) (*fpl.LiveResponse, error)
	EventStatus(ctx context.Context) (*fpl.EventStatusResponse, error)
	TeamHistory(ctx context.Context, teamID int) (*fpl.TeamHistory, error)
	LeagueStandings(ctx context.Context, leagueID int) (*fpl.LeagueStandings, error)
	ManagerTransfers(ctx context.Context, teamID int) ([]fpl.ManagerTransfer, error)
	PlayerSummary(ctx context.Context, playerID int) (*fpl.PlayerSummary, error)
	ManagerStatus(ctx context.Context, teamID int, b *fpl.Bootstrap) (*fpl.ManagerStatus, error)
}

// Engine holds everything the algorithms need as explicit state.
//
// Weights are explicit state, making loading testable and preventing silent
// fallback behavior from changing results unexpectedly.
type Engine struct {
	client  Client
	weights Weights

	// Now is injectable because news age is rendered as a relative string
	// ("2 days ago"). Left as time.Now the output would drift day to day,
	// which would make any golden file containing an injured player flaky.
	Now func() time.Time

	// IntelFetcher is injectable for the same reason Now is: its default
	// implementation makes real HTTP requests to third-party sites
	// (premierleague.com, allaboutfpl.com — see dgw_intel.go), which tests
	// must never do. Chip strategy treats a fetch failure as best-effort and
	// proceeds without it, because chip strategy treats this fetch as best-effort
	// this call.
	IntelFetcher intelFetcher

	// FinishingLuckSource is unlike IntelFetcher: it defaults to nil rather
	// than a real client, because Differentials has no equivalent of chip
	// strategy's "always call it, treat failure as best-effort" contract to
	// fall back on safely across every one of the ~20 existing NewEngine call
	// sites in tests — a nil source means the feature is simply not
	// configured, and Differentials skips it entirely rather than risking an
	// accidental live network call from a test that doesn't know this field
	// exists. cmd/fpl-mcp wires a real *insights.Client in after construction.
	// The signal itself needs olbauday/FPL-Core-Insights' shots.csv, which
	// only covers 2025-26 as of this writing (see internal/insights' doc) —
	// so even wired in, this degrades to no signal for seasons it doesn't
	// cover, exactly like a nil source.
	FinishingLuckSource FinishingLuckSource

	// CongestionSource follows FinishingLuckSource's pattern exactly, for the
	// same reasons: nil by default, wired to a real *insights.Client only by
	// cmd/fpl-mcp, and degrading to no signal (rather than an error) for any
	// season olbauday/FPL-Core-Insights' cross-competition fixtures.csv
	// doesn't cover (2025-26 only as of this writing). Measured via fplctl
	// backtest -congestion (see CHANGELOG.md): a congested-fixture FDR bump
	// matched or beat the baseline captain-pick outcome in every slice
	// tested, but the effect is small, single-season, and the threshold/bump
	// size are untuned — so FixtureOutlook surfaces it as an informational
	// flag rather than folding it into blendFDR, the same "signal exists,
	// weighting it doesn't (yet)" distinction FinishingRegression draws in
	// differentials.go.
	CongestionSource CongestionSource
}

// intelFetcher is the subset of *DGWIntelFetcher chip strategy needs — an
// interface so tests can substitute a stub with no network access.
type intelFetcher interface {
	Fetch(ctx context.Context) (*CommunityIntel, error)
}

// FinishingLuckSource is the subset of *insights.Client Differentials needs
// for the finishing-regression signal — an interface so tests can
// substitute a stub with no network access, matching intelFetcher's
// pattern.
type FinishingLuckSource interface {
	FinishingLuck(ctx context.Context, season string, fromGW, toGW int) (map[int]insights.FinishingDelta, error)
}

// CongestionSource is the subset of *insights.Client FixtureOutlook needs
// for the cross-competition congestion signal — an interface so tests can
// substitute a stub with no network access, matching FinishingLuckSource's
// pattern.
type CongestionSource interface {
	TeamFixtureCalendar(ctx context.Context, season string, fromGW, toGW int) (map[int][]time.Time, error)
}

// NewEngine returns an Engine with the hand-tuned default weights.
// FinishingLuckSource is left nil — see its doc comment for why callers who
// want that feature wire it in explicitly rather than getting it by default.
func NewEngine(c Client) *Engine {
	return &Engine{client: c, weights: DefaultWeights(), Now: time.Now, IntelFetcher: NewDGWIntelFetcher()}
}

// WithWeights returns a copy of the Engine using w. Used by the optimizer and
// by backtests that sweep weight space.
func (e *Engine) WithWeights(w Weights) *Engine {
	c := *e
	c.weights = w
	return &c
}

// Weights returns the active weights.
func (e *Engine) Weights() Weights { return e.weights }

// Streak is the hot/cold assessment returned alongside a pick.
type Streak struct {
	Streak string `json:"streak"`
	Detail string `json:"detail"`
}

// DetectStreak compares recent form against season points-per-game.
//
// Note the guard — with either value at zero the result is "neutral". In
// preseason FPL resets form to 0.0 for every player, so this returns
// "Insufficient data" across the board until the season starts.
func DetectStreak(p *fpl.Player) Streak {
	form := p.Form.Float()
	ppg := p.PointsPerGame.Float()

	if ppg <= 0 || form <= 0 {
		return Streak{Streak: "neutral", Detail: "Insufficient data"}
	}

	ratio := form / ppg
	switch {
	case ratio > 1.3:
		return Streak{
			Streak: "hot",
			Detail: "Form " + FloatStr(form) + " well above season avg " + FloatStr(ppg),
		}
	case ratio < 0.7:
		return Streak{
			Streak: "cold",
			Detail: "Form " + FloatStr(form) + " well below season avg " + FloatStr(ppg),
		}
	default:
		return Streak{
			Streak: "neutral",
			Detail: "Form " + FloatStr(form) + " in line with season avg " + FloatStr(ppg),
		}
	}
}
