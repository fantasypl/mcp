package algo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// include_player_ids (#20): forced players always appear, and the result
// reports what forcing them costs.
func TestOptimalSquadIncludesForcedPlayers(t *testing.T) {
	e := newEngineForOptimalSquad(t, "midseason")
	baseline, err := e.OptimalSquad(context.Background(), 1000, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Include != nil {
		t.Errorf("Include = %+v, want nil without include_player_ids", baseline.Include)
	}
	inBaseline := map[int]bool{}
	for _, s := range baseline.Squad {
		inBaseline[s.ID] = true
	}
	// A cheap available midfielder the baseline leaves out.
	b := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json"))
	forceID := 0
	for _, p := range b.Elements {
		if p.ElementType == 3 && p.Status == "a" && p.NowCost <= 50 && !inBaseline[p.ID] {
			forceID = p.ID
			break
		}
	}
	if forceID == 0 {
		t.Fatal("no suitable midfielder to force in the fixture")
	}

	got, err := e.OptimalSquad(context.Background(), 1000, nil, nil, []int{forceID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range got.Squad {
		found = found || s.ID == forceID
	}
	if !found {
		t.Fatalf("forced player %d missing from the squad", forceID)
	}
	inc := got.Include
	if inc == nil {
		t.Fatal("Include = nil, want the cost of the forced player")
	}
	if inc.ProjectedPointsWithout != baseline.ProjectedPoints {
		t.Errorf("ProjectedPointsWithout = %v, want the unforced squad's %v", inc.ProjectedPointsWithout, baseline.ProjectedPoints)
	}
	if want := Round(baseline.ProjectedPoints-got.ProjectedPoints, 2); inc.PointsCost != want || inc.PointsCost < 0 {
		t.Errorf("PointsCost = %v, want %v (and never negative)", inc.PointsCost, want)
	}
	t.Logf("forced %d: projected %.2f, without %.2f, cost %.2f", forceID, got.ProjectedPoints, inc.ProjectedPointsWithout, inc.PointsCost)
}

func TestOptimalSquadRejectsImpossibleIncludes(t *testing.T) {
	e := newEngineForOptimalSquad(t, "midseason")
	b := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json"))
	var gkps, pricey, club4 []int
	for _, p := range b.Elements {
		if p.ElementType == 1 && len(gkps) < 3 {
			gkps = append(gkps, p.ID)
		}
	}
	// Four outfielders from the first club, one or two per position, so
	// only the club cap is broken.
	perPosClub := map[int]int{}
	for _, p := range b.Elements {
		if p.Team == b.Elements[0].Team && p.ElementType > 1 && perPosClub[p.ElementType] < 2 && len(club4) < 4 {
			club4 = append(club4, p.ID)
			perPosClub[p.ElementType]++
		}
	}
	// Two of the most expensive players per position, within quota, until
	// they pass £100m.
	cost := 0
	perPos := map[int]int{}
	for cost <= 1000 {
		best := -1
		for i, p := range b.Elements {
			if perPos[p.ElementType] >= fplQuota[p.ElementType] || (best >= 0 && p.NowCost <= b.Elements[best].NowCost) {
				continue
			}
			if containsID(pricey, p.ID) {
				continue
			}
			best = i
		}
		if best < 0 {
			break
		}
		p := b.Elements[best]
		pricey = append(pricey, p.ID)
		perPos[p.ElementType]++
		cost += p.NowCost
	}

	cases := []struct {
		name             string
		include, exclude []int
		want             string
	}{
		{"three goalkeepers", gkps, nil, "GKP"},
		{"four from one club", club4, nil, "at most 3 from one club"},
		{"in include and exclude", gkps[:1], gkps[:1], "both include_player_ids and exclude_player_ids"},
		{"unknown id", []int{999999}, nil, "no player has id"},
		{"over budget", pricey, nil, "over the £100.0m budget"},
		// Under budget on their own, but too dear to fill the other places.
		{"leaves too little for the rest", pricey[:len(pricey)-1], nil, "leaves too little to fill the other"},
	}
	for _, tc := range cases {
		_, err := e.OptimalSquad(context.Background(), 1000, nil, tc.exclude, tc.include)
		var reqErr *SquadRequestError
		if !errors.As(err, &reqErr) {
			t.Errorf("%s: err = %v, want a *SquadRequestError", tc.name, err)
			continue
		}
		if !strings.Contains(reqErr.Msg, tc.want) {
			t.Errorf("%s: message %q, want it to mention %q", tc.name, reqErr.Msg, tc.want)
		}
	}
}

func containsID(ids []int, id int) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
