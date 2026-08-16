// Package algo ports the FPL scoring algorithms from app/algorithms/*.py.
//
// Two structural changes from the Python, both deliberate:
//
//   - Algorithms are methods on Engine rather than module-level functions, so
//     weights and the FPL client are injected instead of loaded at import time
//     behind a swallowed exception.
//   - Players arrive as typed fpl.Player values rather than dicts.
//
// Everything else is a faithful translation. Where a Python built-in behaves
// differently from its Go counterpart — rounding, capitalisation, sort
// stability — see pyfmt.go and the comments at each call site.
package algo

import (
	"context"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
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

// Position returns the short position name, or "?" as the Python .get default.
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
}

// Engine holds everything the algorithms need, replacing the Python's
// module-level globals.
//
// captain.py runs `WEIGHTS = _load_weights()` at import time, reading
// data/optimized_weights.json behind a bare `except: pass`. That is both
// untestable and silently variable, so weights become explicit state here.
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
	// proceeds without it, matching the Python's own bare try/except around
	// this call.
	IntelFetcher intelFetcher
}

// intelFetcher is the subset of *DGWIntelFetcher chip strategy needs — an
// interface so tests can substitute a stub with no network access.
type intelFetcher interface {
	Fetch(ctx context.Context) (*CommunityIntel, error)
}

// NewEngine returns an Engine with the hand-tuned default weights.
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

// DetectStreak ports algorithms.detect_streak: compare recent form against
// season points-per-game.
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
