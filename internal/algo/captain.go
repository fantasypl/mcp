package algo

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// Captain Pick algorithm v3.0 — multiplicative fixture model.
//
// score = compressed_base × avg_fixture_multiplier × dgw_factor
//
// The multiplicative form is the whole point of v3.0: additive fixture scoring
// meant a high-base player outscored everyone regardless of opponent. Here a
// bad fixture scales the score down proportionally, so the better the player,
// the more a hard fixture costs in absolute terms.

// TeamFixture is one fixture from a team's perspective.
type TeamFixture struct {
	FDR      float64
	IsHome   bool
	Opponent int
}

// buildFixtureMap indexes each team's upcoming fixtures by team ID.
//
// Returns team id -> fixtures in that gameweek. A team appears more than once
// in a double gameweek. Iteration follows the fixtures slice order, which the
// used for the "first fixture" fields in the output.
func buildFixtureMap(fixtures []fpl.Fixture, gameweek int, teams map[int]*fpl.Team) map[int][]TeamFixture {
	out := make(map[int][]TeamFixture)

	for i := range fixtures {
		f := &fixtures[i]
		if !f.InGameweek(gameweek) {
			continue
		}

		homeFDR := float64(f.TeamHDifficulty)
		awayFDR := float64(f.TeamADifficulty)

		if teams != nil {
			// What matters for captaincy is how weak the opponent's defence
			// is, not how strong their attack is.
			homeFDR = blendFDR(homeFDR, strengthOr(teams[f.TeamA], func(t *fpl.Team) int { return t.StrengthDefenceAway }))
			awayFDR = blendFDR(awayFDR, strengthOr(teams[f.TeamH], func(t *fpl.Team) int { return t.StrengthDefenceHome }))
		}

		out[f.TeamH] = append(out[f.TeamH], TeamFixture{FDR: homeFDR, IsHome: true, Opponent: f.TeamA})
		out[f.TeamA] = append(out[f.TeamA], TeamFixture{FDR: awayFDR, IsHome: false, Opponent: f.TeamH})
	}
	return out
}

// strengthOr returns 1200 when the team or field is absent:
// a missing team, or a missing field, falls back to 1200.
func strengthOr(t *fpl.Team, get func(*fpl.Team) int) int {
	if t == nil {
		return 1200
	}
	return get(t)
}

// blendFDR blends 40% raw FDR with 60% normalised opponent strength. FPL's
// strength values run ~1000-1400 with finer granularity than
// the static 1-5 FDR, and they update weekly.
func blendFDR(rawFDR float64, opponentStrength int) float64 {
	strengthNorm := math.Max(1.0, math.Min(5.0, float64(opponentStrength-1000)/100+1.0))
	return Round(rawFDR*0.4+strengthNorm*0.6, 2)
}

// playingChancePenalty scores how much to discount a player based on their
// chance of playing.
//
// The null-versus-zero distinction is the crux: a nil chance means no flag has
// been raised, so the player is assumed fit unless status says otherwise. A
// chance of 0 means definitely out and earns the full penalty.
func (e *Engine) playingChancePenalty(p *fpl.Player) float64 {
	if p.ChanceOfPlayingNextRound == nil {
		status := p.Status
		if status == "" {
			status = "a"
		}
		if InjuryStatuses[status] {
			return e.weights.PlayingChanceMaxPenalty
		}
		return 0.0
	}
	chance := float64(*p.ChanceOfPlayingNextRound)
	return e.weights.PlayingChanceMaxPenalty * (1.0 - chance/100.0)
}

// scorePlayer computes a player's captain score from form, fixtures, and
// playing-chance.
func (e *Engine) scorePlayer(p *fpl.Player, fixtures []TeamFixture) float64 {
	w := e.weights

	form := p.Form.Float()
	ppg := p.PointsPerGame.Float()
	ict := p.ICTIndex.Float()

	nineties := 0.0
	if p.Minutes > 0 {
		nineties = float64(p.Minutes) / 90.0
	}

	xgPer90, xaPer90 := 0.0, 0.0
	if nineties > 0 {
		xgPer90 = p.ExpectedGoals.Float() / nineties
		xaPer90 = p.ExpectedAssists.Float() / nineties
	}

	// bonus / starts, with starts floored at 1.
	bonusPG := float64(p.Bonus) / float64(max(1, p.Starts))

	penaltyNorm := 0.0
	if p.PenaltiesOrder != nil && *p.PenaltiesOrder == 1 {
		penaltyNorm = 1.0
	}

	epNext := p.EPNext.Float()

	// Minutes certainty is deliberately not clamped: a player who started more
	// often than his rounded 90s can exceed 1.0, as required by the scoring rule.
	gwPlayed := 1
	if nineties > 0 {
		gwPlayed = max(1, RoundToInt(nineties))
	}
	minutesCert := float64(p.Starts) / float64(max(1, gwPlayed))

	chancePenalty := e.playingChancePenalty(p)
	newsPen := NewsPenaltyScore(p) * w.NewsPenalty

	// Defensive contribution counts for DEF and MID only.
	defContribPer90 := 0.0
	if p.ElementType == 2 || p.ElementType == 3 {
		defContribPer90 = p.DefensiveContributionPer90.Float()
	}

	setPieceNorm := 0.0
	corners := orderOf(p.CornersAndIndirectFreekicksOrder)
	fks := orderOf(p.DirectFreekicksOrder)
	switch {
	case corners == 1 && fks == 1:
		setPieceNorm = 1.0
	case corners == 1 || fks == 1:
		setPieceNorm = 0.6
	case corners == 2 || fks == 2:
		setPieceNorm = 0.2
	}

	dreamteamRate := 0.0
	if p.Starts > 0 {
		dreamteamRate = float64(p.DreamteamCount) / float64(max(1, p.Starts))
	}
	dreamteamNorm := Normalize(dreamteamRate, 0, 0.3)

	// Every factor is normalised to 0-1 first so no term dominates on raw scale.
	baseScore := Normalize(xgPer90, 0, 1.0)*w.XG90 +
		Normalize(xaPer90, 0, 0.5)*w.XA90 +
		Normalize(form, 0, 10)*w.Form +
		Normalize(ppg, 0, 10)*w.PPG +
		Normalize(epNext, 0, 10)*w.EPNext +
		Normalize(ict, 0, 300)*w.ICT +
		Normalize(bonusPG, 0, 3)*w.BonusPG +
		penaltyNorm*w.Penalty +
		setPieceNorm*w.SetPiece +
		dreamteamNorm*w.Dreamteam +
		minutesCert*w.MinutesCert +
		Normalize(defContribPer90, 0, 5.0)*w.DefContrib +
		chancePenalty +
		newsPen

	// Compress so fixtures can actually swing the ranking.
	compressedBase := 0.0
	if baseScore > 0 {
		compressedBase = math.Pow(baseScore, 0.9)
	}

	var score float64
	if len(fixtures) > 0 {
		multiplier := 0.0
		for _, f := range fixtures {
			// FDR 1 -> +fdr_weight, FDR 3 -> 0, FDR 5 -> -fdr_weight
			fdrBonus := (3 - f.FDR) / 2.0 * w.FDR
			homeBonus := 0.0
			if f.IsHome {
				homeBonus = w.Home
			}
			multiplier += 1.0 + fdrBonus + homeBonus
		}
		avg := multiplier / float64(len(fixtures))
		// A double gameweek is proportionally more expected points.
		score = compressedBase * avg * float64(len(fixtures))
	} else {
		// Blanking: penalise heavily rather than excluding, so the caller sees why.
		score = compressedBase * 0.1
	}

	return Round(score, 3)
}

func orderOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// buildReasoning composes a human-readable explanation for a captain pick.
//
// The final Capitalize lower-cases
// everything after the first character — so "xG/90" becomes "xg/90" and "FPL"
// becomes "fpl" in the output. That is not a bug to fix here; it is the
// reference behaviour the golden files record.
func (e *Engine) buildReasoning(p *fpl.Player, fixtures []TeamFixture, score float64) string {
	var parts []string

	form := p.Form.Float()
	switch {
	case form >= 7:
		parts = append(parts, "exceptional form")
	case form >= 5:
		parts = append(parts, "strong form")
	case form <= 2:
		parts = append(parts, "poor form")
	}

	if p.Minutes > 0 {
		nineties := float64(p.Minutes) / 90.0
		xg90, xa90 := 0.0, 0.0
		if nineties > 0 {
			xg90 = p.ExpectedGoals.Float() / nineties
			xa90 = p.ExpectedAssists.Float() / nineties
		}
		if xg90 >= 0.5 {
			parts = append(parts, fmt.Sprintf("elite xG/90 (%.2f)", xg90))
		} else if xg90 >= 0.3 {
			parts = append(parts, fmt.Sprintf("strong xG/90 (%.2f)", xg90))
		}
		if xa90 >= 0.3 {
			parts = append(parts, fmt.Sprintf("strong xA/90 (%.2f)", xa90))
		}
	}

	if orderOf(p.PenaltiesOrder) == 1 {
		parts = append(parts, "on penalties")
	}

	corners := orderOf(p.CornersAndIndirectFreekicksOrder)
	fks := orderOf(p.DirectFreekicksOrder)
	switch {
	case corners == 1 && fks == 1:
		parts = append(parts, "on corners + free kicks")
	case corners == 1:
		parts = append(parts, "on corners")
	case fks == 1:
		parts = append(parts, "on direct free kicks")
	}

	epNext := p.EPNext.Float()
	if epNext >= 6 {
		parts = append(parts, "FPL predicts "+FloatStr(epNext)+"pts")
	} else if epNext >= 4 {
		parts = append(parts, "FPL expects "+FloatStr(epNext)+"pts")
	}

	if len(fixtures) > 0 {
		if len(fixtures) > 1 {
			parts = append(parts, fmt.Sprintf("double gameweek (%d fixtures)", len(fixtures)))
		}
		for _, f := range fixtures {
			// Float formatting truncates toward zero: FDR 1.4 renders as 1.
			if f.FDR <= 2 {
				parts = append(parts, fmt.Sprintf("easy fixture (FDR %d)", TruncInt(f.FDR)))
			} else if f.FDR >= 4 {
				parts = append(parts, fmt.Sprintf("tough fixture (FDR %d)", TruncInt(f.FDR)))
			}
			if f.IsHome {
				parts = append(parts, "home advantage")
			}
		}
	} else {
		parts = append(parts, "NO FIXTURE this GW — do not captain")
	}

	status := p.Status
	if status == "" {
		status = "a"
	}
	if InjuryStatuses[status] {
		if p.ChanceOfPlayingNextRound != nil {
			parts = append(parts, fmt.Sprintf("injury concern (%d%% chance)", *p.ChanceOfPlayingNextRound))
		} else {
			parts = append(parts, "injury concern")
		}
	}

	if p.ICTIndex.Float() >= 150 {
		parts = append(parts, "elite ICT index")
	}

	if news := FormatNewsForReasoning(p, e.Now()); news != "" {
		parts = append(parts, "news: "+news)
	}

	if len(parts) == 0 {
		parts = append(parts, "solid all-round score")
	}

	return Capitalize(strings.Join(parts, ", ")) + " (score: " + FloatStr(score) + ")"
}

// ---------------------------------------------------------------------------
// Output shapes. Field order here does not matter — the golden comparison is
// structural — but the json tags and null-vs-absent behaviour do.
// ---------------------------------------------------------------------------

type CaptainResult struct {
	Gameweek         int            `json:"gameweek"`
	AlgorithmVersion string         `json:"algorithm_version"`
	MostCaptained    *MostCaptained `json:"most_captained"`
	NumPicks         int            `json:"num_picks"`
	Picks            []CaptainPick  `json:"picks"`
}

type MostCaptained struct {
	PlayerID      int      `json:"player_id"`
	Name          string   `json:"name"`
	Team          string   `json:"team"`
	SelectedByPct float64  `json:"selected_by_pct"`
	CaptaincyPct  *float64 `json:"captaincy_pct"`
}

type CaptainPick struct {
	Rank      int          `json:"rank"`
	Player    PlayerBrief  `json:"player"`
	Fixture   *FixtureInfo `json:"fixture"`
	Score     float64      `json:"score"`
	Reasoning string       `json:"reasoning"`
	Stats     CaptainStats `json:"stats"`
	Streak    Streak       `json:"streak"`
}

type PlayerBrief struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Team          string  `json:"team"`
	TeamFullName  string  `json:"team_full_name"`
	Position      string  `json:"position"`
	Cost          float64 `json:"cost"`
	SelectedByPct float64 `json:"selected_by_pct"`
	Status        string  `json:"status"`
}

type FixtureEntry struct {
	Opponent string  `json:"opponent"`
	Venue    string  `json:"venue"`
	FDR      float64 `json:"fdr"`
}

type FixtureInfo struct {
	Fixtures []FixtureEntry `json:"fixtures"`
	Gameweek int            `json:"gameweek"`
	IsDGW    bool           `json:"is_dgw"`
	// Retained for backward compatibility: mirrors the first fixture.
	Opponent string  `json:"opponent"`
	Venue    string  `json:"venue"`
	FDR      float64 `json:"fdr"`
}

type CaptainStats struct {
	Form                       float64 `json:"form"`
	PointsPerGame              float64 `json:"points_per_game"`
	EPNext                     float64 `json:"ep_next"`
	ICTIndex                   float64 `json:"ict_index"`
	TotalPoints                int     `json:"total_points"`
	Bonus                      int     `json:"bonus"`
	ExpectedGoals              float64 `json:"expected_goals"`
	ExpectedAssists            float64 `json:"expected_assists"`
	ExpectedGoalInvolvements   float64 `json:"expected_goal_involvements"`
	XGPer90                    float64 `json:"xg_per_90"`
	XAPer90                    float64 `json:"xa_per_90"`
	DefensiveContributionPer90 float64 `json:"defensive_contribution_per_90"`
	PenaltiesOrder             *int    `json:"penalties_order"`
	Starts                     int     `json:"starts"`
	ChanceOfPlaying            *int    `json:"chance_of_playing"`
}

// maxPerTeam caps how many picks may come from one club. Without it three or
// four picks routinely come from the same side, which is useless as advice.
const maxPerTeam = 2

type scoredPlayer struct {
	score    float64
	player   *fpl.Player
	fixtures []TeamFixture
}

// ScoredPlayer pairs a player with its raw captain score and that
// gameweek's fixtures — the uncapped, unfiltered form CaptainPicks builds
// internally before excluding blank-gameweek players and applying the
// max-2-per-club business rule. Public for callers (fplctl's evaluate and
// audit subcommands) that need the same raw scoring applied directly,
// without either of CaptainPicks' rules.
type ScoredPlayer struct {
	Player   *fpl.Player
	Score    float64
	Fixtures []TeamFixture
}

// ScoreAllPlayers exposes the scoring loop that fplctl's evaluate and audit
// subcommands both inline directly, rather than calling CaptainPicks: every
// player is scored, including
// ones with no fixture this gameweek (scorePlayer's blanking penalty handles
// those), and the result is stable-sorted descending by score with no
// per-club cap. gameweek is optional; nil selects the next gameweek.
func (e *Engine) ScoreAllPlayers(ctx context.Context, gameweek *int) ([]ScoredPlayer, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	gw := bootstrap.NextGameweek()
	if gameweek != nil {
		gw = *gameweek
	}

	teams := teamsByID(bootstrap)
	fixtureMap := buildFixtureMap(fixtures, gw, teams)

	scored := make([]ScoredPlayer, 0, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		p := &bootstrap.Elements[i]
		pf := fixtureMap[p.Team]
		scored = append(scored, ScoredPlayer{Player: p, Score: e.scorePlayer(p, pf), Fixtures: pf})
	}
	slices.SortStableFunc(scored, func(a, b ScoredPlayer) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		default:
			return 0
		}
	})
	return scored, nil
}

// CaptainPicks returns the top-N captain recommendations for a gameweek.
//
// gameweek is optional; nil selects the next gameweek. topN defaults to 5 when
// non-positive, using the function's default horizon.
func (e *Engine) CaptainPicks(ctx context.Context, gameweek *int, topN int) (*CaptainResult, error) {
	if topN <= 0 {
		topN = 5
	}

	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	gw := bootstrap.NextGameweek()
	if gameweek != nil {
		gw = *gameweek
	}

	teams := teamsByID(bootstrap)
	fixtureMap := buildFixtureMap(fixtures, gw, teams)

	scored := make([]scoredPlayer, 0, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		p := &bootstrap.Elements[i]
		pf := fixtureMap[p.Team]
		if len(pf) == 0 {
			continue // blank gameweek
		}
		scored = append(scored, scoredPlayer{e.scorePlayer(p, pf), p, pf})
	}

	// Stable sort is required, not merely preferred: equal-score entries retain
	// and in preseason every player has form 0.0, so ties are pervasive and
	// stability alone decides the ordering.
	slices.SortStableFunc(scored, func(a, b scoredPlayer) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		default:
			return 0
		}
	})

	top := make([]scoredPlayer, 0, topN)
	perTeam := make(map[int]int)
	for _, s := range scored {
		if perTeam[s.player.Team] >= maxPerTeam {
			continue
		}
		top = append(top, s)
		perTeam[s.player.Team]++
		if len(top) >= topN {
			break
		}
	}

	picks := make([]CaptainPick, 0, len(top))
	for _, s := range top {
		picks = append(picks, e.buildPick(s, gw, teams, len(picks)+1))
	}

	return &CaptainResult{
		Gameweek:         gw,
		AlgorithmVersion: "3.0",
		MostCaptained:    mostCaptained(bootstrap, gw, teams),
		NumPicks:         len(picks),
		Picks:            picks,
	}, nil
}

func (e *Engine) buildPick(s scoredPlayer, gw int, teams map[int]*fpl.Team, rank int) CaptainPick {
	p := s.player
	team := teams[p.Team]

	var info *FixtureInfo
	if len(s.fixtures) > 0 {
		entries := make([]FixtureEntry, 0, len(s.fixtures))
		for _, f := range s.fixtures {
			venue := "Away"
			if f.IsHome {
				venue = "Home"
			}
			entries = append(entries, FixtureEntry{
				Opponent: shortName(teams[f.Opponent]),
				Venue:    venue,
				FDR:      f.FDR,
			})
		}
		info = &FixtureInfo{
			Fixtures: entries,
			Gameweek: gw,
			IsDGW:    len(entries) > 1,
			Opponent: entries[0].Opponent,
			Venue:    entries[0].Venue,
			FDR:      entries[0].FDR,
		}
	}

	nineties := 0.0
	if p.Minutes > 0 {
		nineties = float64(p.Minutes) / 90.0
	}
	xg := p.ExpectedGoals.Float()
	xa := p.ExpectedAssists.Float()
	xg90, xa90 := 0.0, 0.0
	if nineties > 0 {
		xg90 = Round(xg/nineties, 3)
		xa90 = Round(xa/nineties, 3)
	}

	status := p.Status
	if status == "" {
		status = "a"
	}

	return CaptainPick{
		Rank: rank,
		Player: PlayerBrief{
			ID:            p.ID,
			Name:          p.WebName,
			Team:          shortName(team),
			TeamFullName:  fullName(team),
			Position:      Position(p.ElementType),
			Cost:          float64(p.NowCost) / 10,
			SelectedByPct: p.SelectedByPercent.Float(),
			Status:        status,
		},
		Fixture:   info,
		Score:     s.score,
		Reasoning: e.buildReasoning(p, s.fixtures, s.score),
		Stats: CaptainStats{
			Form:                       p.Form.Float(),
			PointsPerGame:              p.PointsPerGame.Float(),
			EPNext:                     p.EPNext.Float(),
			ICTIndex:                   p.ICTIndex.Float(),
			TotalPoints:                p.TotalPoints,
			Bonus:                      p.Bonus,
			ExpectedGoals:              xg,
			ExpectedAssists:            xa,
			ExpectedGoalInvolvements:   p.ExpectedGoalInvolvements.Float(),
			XGPer90:                    xg90,
			XAPer90:                    xa90,
			DefensiveContributionPer90: p.DefensiveContributionPer90.Float(),
			PenaltiesOrder:             p.PenaltiesOrder,
			Starts:                     p.Starts,
			ChanceOfPlaying:            p.ChanceOfPlayingNextRound,
		},
		Streak: DetectStreak(p),
	}
}

func mostCaptained(b *fpl.Bootstrap, gw int, teams map[int]*fpl.Team) *MostCaptained {
	for i := range b.Events {
		ev := &b.Events[i]
		if ev.ID != gw {
			continue
		}
		if ev.MostCaptained == nil || *ev.MostCaptained == 0 {
			return nil
		}
		for j := range b.Elements {
			p := &b.Elements[j]
			if p.ID != *ev.MostCaptained {
				continue
			}
			return &MostCaptained{
				PlayerID:      *ev.MostCaptained,
				Name:          p.WebName,
				Team:          shortName(teams[p.Team]),
				SelectedByPct: p.SelectedByPercent.Float(),
				// most_captained_pct is not present in the FPL payload, so the
				// A missing value is represented as nil here.
				CaptaincyPct: nil,
			}
		}
		return nil
	}
	return nil
}

func teamsByID(b *fpl.Bootstrap) map[int]*fpl.Team {
	m := make(map[int]*fpl.Team, len(b.Teams))
	for i := range b.Teams {
		m[b.Teams[i].ID] = &b.Teams[i]
	}
	return m
}

func shortName(t *fpl.Team) string {
	if t == nil {
		return "?"
	}
	return t.ShortName
}

func fullName(t *fpl.Team) string {
	if t == nil {
		return "?"
	}
	return t.Name
}
