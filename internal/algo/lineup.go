package algo

import (
	"fmt"
	"slices"
)

// lineup is a starting XI, its formation, and the bench order, chosen from a
// 15-man squad.
type lineup struct {
	// XI is the eleven starter ids, goalkeeper first, then defenders,
	// midfielders and forwards, each best-projected first.
	XI []int
	// Formation is "defenders-midfielders-forwards", e.g. "3-5-2".
	Formation string
	// Bench is the four bench ids: the spare goalkeeper, then outfield players
	// by descending projected points.
	Bench []int
}

// Valid FPL formations put 3-5 defenders, 2-5 midfielders and 1-3 forwards
// alongside the goalkeeper. Enumerating them is cheap and makes the choice
// easy to verify.
const (
	minDef, maxDef = 3, 5
	minMid, maxMid = 2, 5
	minFwd, maxFwd = 1, 3
)

// chooseLineup picks the starting XI that maximizes total projected points
// under FPL's formation rules, and orders the bench.
//
// Within a position the best-projected players always start, so each formation
// scores as the sum of the top n of each position, and the best formation wins.
// Ties go to the first formation in enumeration order (fewest defenders, then
// fewest midfielders), so the result is deterministic.
//
// Returns the zero lineup if the squad cannot fill any valid formation.
func chooseLineup(squad []OptimalSquadSlot) lineup {
	byPos := map[string][]OptimalSquadSlot{}
	for _, s := range squad {
		byPos[s.Position] = append(byPos[s.Position], s)
	}
	for _, players := range byPos {
		// Best first; equal projections fall back to ID for a stable result.
		slices.SortStableFunc(players, func(a, b OptimalSquadSlot) int {
			switch {
			case a.ProjectedPoints > b.ProjectedPoints:
				return -1
			case a.ProjectedPoints < b.ProjectedPoints:
				return 1
			default:
				return a.ID - b.ID
			}
		})
	}
	gkps, defs, mids, fwds := byPos["GKP"], byPos["DEF"], byPos["MID"], byPos["FWD"]
	if len(gkps) == 0 {
		return lineup{}
	}

	sumTop := func(players []OptimalSquadSlot, n int) float64 {
		total := 0.0
		for _, p := range players[:n] {
			total += p.ProjectedPoints
		}
		return total
	}

	bestScore := -1.0
	var bestDef, bestMid, bestFwd int
	for d := minDef; d <= maxDef; d++ {
		for m := minMid; m <= maxMid; m++ {
			f := 10 - d - m
			if f < minFwd || f > maxFwd {
				continue
			}
			if d > len(defs) || m > len(mids) || f > len(fwds) {
				continue
			}
			score := sumTop(defs, d) + sumTop(mids, m) + sumTop(fwds, f)
			if score > bestScore {
				bestScore, bestDef, bestMid, bestFwd = score, d, m, f
			}
		}
	}
	if bestScore < 0 {
		return lineup{}
	}

	var out lineup
	out.Formation = fmt.Sprintf("%d-%d-%d", bestDef, bestMid, bestFwd)
	out.XI = append(out.XI, gkps[0].ID)
	var benchOutfield []OptimalSquadSlot
	for _, group := range []struct {
		players []OptimalSquadSlot
		n       int
	}{{defs, bestDef}, {mids, bestMid}, {fwds, bestFwd}} {
		for _, p := range group.players[:group.n] {
			out.XI = append(out.XI, p.ID)
		}
		benchOutfield = append(benchOutfield, group.players[group.n:]...)
	}

	// Any spare goalkeepers open the bench, since FPL requires the bench
	// goalkeeper in the first bench slot.
	for _, g := range gkps[1:] {
		out.Bench = append(out.Bench, g.ID)
	}
	slices.SortStableFunc(benchOutfield, func(a, b OptimalSquadSlot) int {
		switch {
		case a.ProjectedPoints > b.ProjectedPoints:
			return -1
		case a.ProjectedPoints < b.ProjectedPoints:
			return 1
		case PositionOrder[a.Position] != PositionOrder[b.Position]:
			return PositionOrder[a.Position] - PositionOrder[b.Position]
		default:
			return a.ID - b.ID
		}
	})
	for _, p := range benchOutfield {
		out.Bench = append(out.Bench, p.ID)
	}
	return out
}
