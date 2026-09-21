package algo

// playersNeedingReplacement returns the ids squad_health flags as needing a
// transfer suggestion: poor-form starters, and injured or suspended players.
//
// A tough fixture alone does not qualify, since it is a one-week problem. Nor
// does a doubtful bench player: they still appear in squad_health, but forcing
// a suggestion for every 75%-fit reserve would bury the ones that matter. A
// doubtful starter does qualify, and so does an injured or suspended player
// anywhere in the squad.
func playersNeedingReplacement(squad []HubSquadEntry) []int {
	var ids []int
	for _, s := range squad {
		poorFormStarter := s.Starter && s.Form <= 2.0
		unavailable := InjuryStatuses[s.Status] && (s.Starter || s.Status != "d")
		if poorFormStarter || unavailable {
			ids = append(ids, s.ElementID)
		}
	}
	return ids
}
