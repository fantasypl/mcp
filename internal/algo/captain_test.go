package algo

import (
	"context"
	"testing"

	"github.com/ajitem/fpl-intelligence/internal/golden"
)

// The parity gate for the keystone algorithm. chips, scout and transfers all
// consume this scoring, so a discrepancy here propagates everywhere.
func TestCaptainPicksMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, suffix string) {
		cases := []struct {
			name   string
			gw     *int
			topN   int
			golden string
		}{
			{"gw1", ptr(1), 0, "captain_gw1"},
			{"default gameweek", nil, 0, "captain_default"},
			{"top 10", ptr(1), 10, "captain_top10"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := e.CaptainPicks(context.Background(), tc.gw, tc.topN)
				if err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, goldenPath(tc.golden+suffix), got)
			})
		}
	})
}

func TestBlendFDR(t *testing.T) {
	cases := []struct {
		rawFDR   float64
		strength int
		want     float64
	}{
		// 1200 strength normalises to 3.0: 0.4*fdr + 0.6*3.0
		{2, 1200, 2.6},
		{5, 1200, 3.8},
		// Strength clamps to the 1-5 band at both ends.
		{3, 500, 1.8},  // normalised 1.0 -> 0.4*3 + 0.6*1
		{3, 9000, 4.2}, // normalised 5.0 -> 0.4*3 + 0.6*5
		{1, 1000, 1.0},
	}
	for _, tc := range cases {
		if got := blendFDR(tc.rawFDR, tc.strength); got != tc.want {
			t.Errorf("blendFDR(%v, %d) = %v, want %v", tc.rawFDR, tc.strength, got, tc.want)
		}
	}
}

// A missing team must fall back to strength 1200, matching the Python's
// `.get(id, {}).get(field, 1200)` chain.
func TestStrengthFallback(t *testing.T) {
	if got := strengthOr(nil, nil); got != 1200 {
		t.Errorf("missing team strength = %d, want 1200", got)
	}
}

// The highest-risk translation in the port: nil chance means fit, 0 means out.
func TestPlayingChancePenalty(t *testing.T) {
	e := NewEngine(nil)
	maxPen := e.weights.PlayingChanceMaxPenalty

	cases := []struct {
		name   string
		chance *int
		status string
		want   float64
	}{
		{"nil chance, available", nil, "a", 0},
		{"nil chance, injured", nil, "i", maxPen},
		{"nil chance, doubtful", nil, "d", maxPen},
		{"nil chance, suspended", nil, "s", maxPen},
		{"nil chance, empty status defaults to available", nil, "", 0},
		{"chance 100", ptr(100), "a", 0},
		{"chance 75", ptr(75), "d", maxPen * 0.25},
		{"chance 50", ptr(50), "d", maxPen * 0.5},
		{"chance 25", ptr(25), "d", maxPen * 0.75},
		{"chance 0 is fully penalised", ptr(0), "d", maxPen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlayer()
			p.ChanceOfPlayingNextRound = tc.chance
			p.Status = tc.status
			if got := e.playingChancePenalty(p); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// nil and 0 must not be interchangeable; conflating them inverts the penalty
// for every fit player in the pool.
func TestNilChanceIsNotZeroChance(t *testing.T) {
	e := NewEngine(nil)
	fit := newPlayer()
	fit.ChanceOfPlayingNextRound = nil

	out := newPlayer()
	out.ChanceOfPlayingNextRound = ptr(0)

	if e.playingChancePenalty(fit) == e.playingChancePenalty(out) {
		t.Fatal("nil chance and 0 chance produced the same penalty")
	}
	if e.playingChancePenalty(fit) != 0 {
		t.Error("a player with no injury flag should not be penalised")
	}
}

func TestSetPieceScoring(t *testing.T) {
	e := NewEngine(nil)
	score := func(corners, fks *int) float64 {
		p := newPlayer()
		p.CornersAndIndirectFreekicksOrder = corners
		p.DirectFreekicksOrder = fks
		return e.scorePlayer(p, []TeamFixture{{FDR: 3, IsHome: false}})
	}
	both := score(ptr(1), ptr(1))
	one := score(ptr(1), nil)
	secondary := score(ptr(2), nil)
	none := score(nil, nil)

	if !(both > one && one > secondary && secondary > none) {
		t.Errorf("set-piece tiers not ordered: both=%v one=%v secondary=%v none=%v",
			both, one, secondary, none)
	}
}

// A blanking player is scored at a tenth rather than dropped, so callers can
// still see and explain the zero.
func TestNoFixtureIsHeavilyPenalised(t *testing.T) {
	e := NewEngine(nil)
	p := newPlayer()
	withFixture := e.scorePlayer(p, []TeamFixture{{FDR: 3, IsHome: false}})
	blank := e.scorePlayer(p, nil)

	if blank >= withFixture {
		t.Fatalf("blank %v should be far below with-fixture %v", blank, withFixture)
	}
	if want := Round(withFixture*0.1, 3); blank != want {
		t.Errorf("blank score = %v, want %v (a tenth of the fixture score)", blank, want)
	}
}

// A double gameweek should roughly double the score, since dgw_factor is the
// fixture count.
func TestDoubleGameweekScales(t *testing.T) {
	e := NewEngine(nil)
	p := newPlayer()
	single := e.scorePlayer(p, []TeamFixture{{FDR: 3, IsHome: false}})
	double := e.scorePlayer(p, []TeamFixture{{FDR: 3, IsHome: false}, {FDR: 3, IsHome: false}})

	if ratio := double / single; ratio < 1.9 || ratio > 2.1 {
		t.Errorf("DGW/single ratio = %v, want about 2", ratio)
	}
}

// Home advantage and fixture difficulty must both move the score in the
// expected direction — this is the multiplicative model's whole premise.
func TestFixtureMultiplierDirection(t *testing.T) {
	e := NewEngine(nil)
	p := newPlayer()

	easyHome := e.scorePlayer(p, []TeamFixture{{FDR: 1, IsHome: true}})
	easyAway := e.scorePlayer(p, []TeamFixture{{FDR: 1, IsHome: false}})
	hardAway := e.scorePlayer(p, []TeamFixture{{FDR: 5, IsHome: false}})

	if easyHome <= easyAway {
		t.Error("home fixture should score above the same fixture away")
	}
	if easyAway <= hardAway {
		t.Error("easy fixture should score above a hard one")
	}
}

func TestDetectStreak(t *testing.T) {
	cases := []struct {
		name       string
		form, ppg  float64
		wantStreak string
		wantDetail string
	}{
		{"hot", 7.8, 5.0, "hot", "Form 7.8 well above season avg 5.0"},
		{"cold", 2.0, 5.0, "cold", "Form 2.0 well below season avg 5.0"},
		{"neutral", 5.0, 5.0, "neutral", "Form 5.0 in line with season avg 5.0"},
		// Preseason: form resets to zero for everyone.
		{"no form", 0, 5.0, "neutral", "Insufficient data"},
		{"no ppg", 5.0, 0, "neutral", "Insufficient data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlayer()
			p.Form = numOf(tc.form)
			p.PointsPerGame = numOf(tc.ppg)
			got := DetectStreak(p)
			if got.Streak != tc.wantStreak || got.Detail != tc.wantDetail {
				t.Errorf("got %+v, want {%s %s}", got, tc.wantStreak, tc.wantDetail)
			}
		})
	}
}

// Team diversity: no more than two picks may come from one club.
func TestTeamDiversityCap(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, _ string) {
		res, err := e.CaptainPicks(context.Background(), ptr(1), 15)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, p := range res.Picks {
			counts[p.Player.Team]++
		}
		for team, n := range counts {
			if n > maxPerTeam {
				t.Errorf("%s has %d picks, cap is %d", team, n, maxPerTeam)
			}
		}
	})
}
