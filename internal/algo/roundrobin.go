package algo

import "github.com/fantasypl/mcp/internal/fpl"

// RoundRobinFixtures creates the deterministic fixture shape used by the
// offline scenarios: the first half of the participating IDs is paired with
// a cyclically shifted second half.
func RoundRobinFixtures(gw, startID int, teamIDs []int, skip map[int]bool, extra [][2]int) ([]fpl.Fixture, int) {
	ids := make([]int, 0, len(teamIDs))
	for _, id := range teamIDs {
		if !skip[id] {
			ids = append(ids, id)
		}
	}
	half := len(ids) / 2
	shift := gw % half
	away := append(append([]int{}, ids[half:][shift:]...), ids[half:][:shift]...)
	out := make([]fpl.Fixture, 0, half+len(extra))
	id := startID
	for i, h := range ids[:half] {
		finished := gw < 1
		out = append(out, fpl.Fixture{ID: id, Event: &gw, TeamH: h, TeamA: away[i], TeamHDifficulty: 3, TeamADifficulty: 3, Started: finished, Finished: finished, FinishedProvisional: finished, Minutes: 0})
		id++
	}
	for _, pair := range extra {
		out = append(out, fpl.Fixture{ID: id, Event: &gw, TeamH: pair[0], TeamA: pair[1], TeamHDifficulty: 2, TeamADifficulty: 4})
		id++
	}
	return out, id
}
