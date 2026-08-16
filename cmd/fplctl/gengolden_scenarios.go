package main

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/fpl"
)

func scenarioBootstrap(path string, current int, injured map[int]bool) (*fpl.Bootstrap, error) {
	b, err := readFixture[*fpl.Bootstrap](path)
	if err != nil {
		return nil, err
	}
	b.ElementTypes = nil
	for i := range b.Elements {
		if injured[b.Elements[i].ID] {
			b.Elements[i].Status = "i"
		}
	}
	b.Events = make([]fpl.Event, 38)
	for i := range b.Events {
		gw := i + 1
		b.Events[i] = fpl.Event{ID: gw, Finished: gw < current, IsPrevious: gw < current, IsCurrent: gw == current, IsNext: gw == current+1}
	}
	return b, nil
}

func scenarioFixtures() ([]fpl.Fixture, error) {
	b, err := readFixture[*fpl.Bootstrap]("testdata/bootstrap_midseason.json")
	if err != nil {
		return nil, err
	}
	ids := make([]int, len(b.Teams))
	for i, t := range b.Teams {
		ids[i] = t.ID
	}
	sort.Ints(ids)
	var all []fpl.Fixture
	next := 1
	for gw := 1; gw <= 10; gw++ {
		skip := map[int]bool{}
		var extra [][2]int
		if gw == 6 {
			skip[19] = true
			skip[20] = true
		}
		if gw == 5 {
			extra = [][2]int{{15, 8}}
		}
		fx, n := algo.RoundRobinFixtures(gw, next, ids, skip, extra)
		all = append(all, fx...)
		next = n
	}
	all = append(all, fpl.Fixture{ID: next, TeamH: 1, TeamA: 17})
	next++
	for gw := 11; gw <= 38; gw++ {
		fx, n := algo.RoundRobinFixtures(gw, next, ids, nil, nil)
		all = append(all, fx...)
		next = n
	}
	return all, nil
}

type goldenIntel struct{}

func (goldenIntel) Fetch(context.Context) (*algo.CommunityIntel, error) {
	return &algo.CommunityIntel{DGWs: map[string]algo.SourcedMention{"8": {Teams: []string{"AVL"}, Status: "predicted", Sources: []string{"allaboutfpl.com"}}}, BGWs: map[string]algo.SourcedMention{"7": {Teams: []string{"NFO"}, Status: "predicted", Sources: []string{"allaboutfpl.com"}}}, SourcesChecked: []string{"premierleague.com", "allaboutfpl.com"}, Errors: []string{"premierleague.com: failed to fetch"}}, nil
}

func genChips(ctx context.Context, out string) error {
	d := filepath.Join("testdata", "chips_scenario")
	b, err := scenarioBootstrap(filepath.Join("testdata", "bootstrap_midseason.json"), 1, nil)
	if err != nil {
		return err
	}
	b.Events[0].ChipPlays = []fpl.ChipPlay{{ChipName: "3xc", NumPlayed: 12345}}
	f, err := scenarioFixtures()
	if err != nil {
		return err
	}
	p, err := readFixture[*fpl.TeamPicks](filepath.Join(d, "picks.json"))
	if err != nil {
		return err
	}
	h, err := readFixture[*fpl.TeamHistory](filepath.Join(d, "history.json"))
	if err != nil {
		return err
	}
	c := algo.NewStubClient(b, f)
	c.SetTeamPicks(syntheticTeamID, 1, p)
	c.SetHistory(syntheticTeamID, h)
	e := algo.NewEngine(c)
	e.IntelFetcher = goldenIntel{}
	e.Now = func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }
	v, err := e.ChipStrategy(ctx, syntheticTeamID)
	if err != nil {
		return err
	}
	for name, x := range map[string]any{"bootstrap.json": b, "fixtures.json": f, "picks.json": p, "history.json": h, "community_intel.json": mustIntel(), "golden.json": v} {
		if err := writeGolden(filepath.Join(out, "chips_scenario", name), x); err != nil {
			return err
		}
	}
	return nil
}
func mustIntel() *algo.CommunityIntel { v, _ := goldenIntel{}.Fetch(context.Background()); return v }

func makeScenarioPicks(ids []int, bank int) *fpl.TeamPicks {
	p := &fpl.TeamPicks{}
	for i, id := range ids {
		pos := i + 1
		mult := 1
		if i == 0 {
			mult = 2
		}
		if pos > 11 {
			mult = 0
		}
		p.Picks = append(p.Picks, fpl.Pick{Element: id, Position: pos, Multiplier: mult, IsCaptain: i == 0, IsViceCaptain: i == 1})
	}
	p.EntryHistory = fpl.EntryHistory{Bank: bank, EventTransfers: 1, OverallRank: 50000}
	return p
}
func scenarioHistory(points []int, chips []fpl.ChipUsage) *fpl.TeamHistory {
	if chips == nil {
		chips = []fpl.ChipUsage{}
	}
	h := &fpl.TeamHistory{Chips: chips}
	for i, p := range points {
		if i == 4 {
			h.Current = append(h.Current, fpl.HistoryGameweek{Event: 1, Points: p})
		}
	}
	return h
}

func genLeague(ctx context.Context, out string) error {
	inj := map[int]bool{1: true, 3: true}
	b, err := scenarioBootstrap(filepath.Join("testdata", "bootstrap_midseason.json"), 1, inj)
	if err != nil {
		return err
	}
	f, err := readFixture[[]fpl.Fixture](filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		return err
	}
	a, err := readFixture[*fpl.TeamPicks](filepath.Join("testdata", "picks_squad1.json"))
	if err != nil {
		return err
	}
	pb := makeScenarioPicks([]int{384, 2, 6, 200, 229, 388, 389, 397, 427, 428, 480, 78, 106, 25, 26}, 12)
	pc := makeScenarioPicks([]int{1, 3, 7, 9, 10, 14, 15, 20, 21, 27, 28, 29, 32, 33, 34}, 3)
	ha := scenarioHistory([]int{78, 70, 82, 75, 68}, []fpl.ChipUsage{{Name: "wildcard", Event: 3}})
	hb := scenarioHistory([]int{55, 60, 58, 62, 55}, []fpl.ChipUsage{{Name: "wildcard", Event: 2}, {Name: "bboost", Event: 4}})
	hc := scenarioHistory([]int{40, 45, 38, 42, 40}, nil)
	st := &fpl.LeagueStandings{League: fpl.LeagueInfo{ID: 12345, Name: "The Bootleg Bin"}, Standings: fpl.StandingsPage{Results: []fpl.LeagueEntry{{Entry: 999001, EntryName: "Title Chaser", PlayerName: "Alex Chaser", Rank: 1, Total: 1850, EventTotal: 68}, {Entry: 999002, EntryName: "Close Rival", PlayerName: "Sam Rival", Rank: 2, Total: 1842, EventTotal: 55}, {Entry: 999003, EntryName: "Chip Hoarder", PlayerName: "Jo Hoarder", Rank: 3, Total: 1690, EventTotal: 40}, {Entry: 999004, EntryName: "Fetch Failure", PlayerName: "Kim Failure", Rank: 4, Total: 1600, EventTotal: 35}}}}
	c := algo.NewStubClient(b, f)
	c.SetLeague(12345, st)
	c.SetTeamPicks(999001, 1, a)
	c.SetTeamPicks(999002, 1, pb)
	c.SetTeamPicks(999003, 1, pc)
	c.SetHistory(999001, ha)
	c.SetHistory(999002, hb)
	c.SetHistory(999003, hc)
	e := algo.NewEngine(c)
	e.Now = func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }
	v, err := e.AnalyzeLeague(ctx, 12345)
	if err != nil {
		return err
	}
	vals := map[string]any{"bootstrap.json": b, "fixtures.json": f, "standings.json": st, "picks_a.json": a, "picks_b.json": pb, "picks_c.json": pc, "history_a.json": ha, "history_b.json": hb, "history_c.json": hc, "golden.json": v}
	for n, x := range vals {
		if err := writeGolden(filepath.Join(out, "league_scenario", n), x); err != nil {
			return err
		}
	}
	return nil
}

func genRivals(ctx context.Context, out string) error {
	d := "testdata/league_scenario"
	b, err := readFixture[*fpl.Bootstrap](filepath.Join(d, "bootstrap.json"))
	if err != nil {
		return err
	}
	for i := range b.Events {
		gw := b.Events[i].ID
		b.Events[i].Finished = gw < 10
		b.Events[i].IsPrevious = gw < 10
		b.Events[i].IsCurrent = gw == 10
		b.Events[i].IsNext = gw == 11
	}
	f, err := readFixture[[]fpl.Fixture](filepath.Join(d, "fixtures.json"))
	if err != nil {
		return err
	}
	st, err := readFixture[*fpl.LeagueStandings](filepath.Join(d, "standings.json"))
	if err != nil {
		return err
	}
	pa, err := readFixture[*fpl.TeamPicks](filepath.Join(d, "picks_a.json"))
	if err != nil {
		return err
	}
	pb, err := readFixture[*fpl.TeamPicks](filepath.Join(d, "picks_b.json"))
	if err != nil {
		return err
	}
	pc, err := readFixture[*fpl.TeamPicks](filepath.Join(d, "picks_c.json"))
	if err != nil {
		return err
	}
	ta := []fpl.ManagerTransfer{{Event: 3, ElementIn: 401, ElementInCost: 100, ElementOut: 30, ElementOutCost: 55}, {Event: 9, ElementIn: 155, ElementInCost: 65, ElementOut: 4, ElementOutCost: 60}, {Event: 10, ElementIn: 379, ElementInCost: 90, ElementOut: 31, ElementOutCost: 45}}
	tc := []fpl.ManagerTransfer{{Event: 2, ElementIn: 3, ElementInCost: 45, ElementOut: 1, ElementOutCost: 50}, {Event: 5, ElementIn: 7, ElementInCost: 50, ElementOut: 9, ElementOutCost: 45}}
	c := algo.NewStubClient(b, f)
	c.SetLeague(12345, st)
	c.SetTeamPicks(999001, 10, pa)
	c.SetTeamPicks(999002, 10, pb)
	c.SetTeamPicks(999003, 10, pc)
	c.SetTransfers(999001, ta)
	c.SetTransfers(999003, tc)
	e := algo.NewEngine(c)
	e.Now = func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }
	v, err := e.RivalAnalysis(ctx, 12345, 999002)
	if err != nil {
		return err
	}
	for n, x := range map[string]any{"bootstrap.json": b, "fixtures.json": f, "standings.json": st, "picks_a.json": pa, "picks_b.json": pb, "picks_c.json": pc, "transfers_a.json": ta, "transfers_c.json": tc, "golden.json": v} {
		if err := writeGolden(filepath.Join(out, "rivals_scenario", n), x); err != nil {
			return err
		}
	}
	return nil
}
