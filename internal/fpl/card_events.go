package fpl

// CardEvent is one player's cards in one fixture.
type CardEvent struct {
	Player int // element ID
	Team   int // team ID the player belongs to
	Yellow int
	Red    int
}

// CardEvents decodes the yellow/red card entries of the fixture's untyped
// Stats.
//
// FPL reports each stat as {"identifier": ..., "h": [...], "a": [...]}, where
// every list item is {"value": n, "element": playerID} and "h"/"a" say which
// side the player was on. Events are grouped per player, in order of first
// appearance, so a player with a yellow and a red in one match yields a single
// event with both set. Anything that doesn't match this shape is skipped
// rather than treated as an error: the payload is unversioned and a decoding
// failure here should never break the caller.
func (f Fixture) CardEvents() []CardEvent {
	var out []CardEvent
	index := map[int]int{} // player -> position in out

	for _, raw := range f.Stats {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		identifier, _ := entry["identifier"].(string)
		if identifier != "yellow_cards" && identifier != "red_cards" {
			continue
		}
		for _, side := range []struct {
			key  string
			team int
		}{{"h", f.TeamH}, {"a", f.TeamA}} {
			items, _ := entry[side.key].([]any)
			for _, rawItem := range items {
				item, ok := rawItem.(map[string]any)
				if !ok {
					continue
				}
				value, _ := item["value"].(float64)
				element, _ := item["element"].(float64)
				if element == 0 || value == 0 {
					continue
				}
				player := int(element)
				i, seen := index[player]
				if !seen {
					i = len(out)
					index[player] = i
					out = append(out, CardEvent{Player: player, Team: side.team})
				}
				if identifier == "yellow_cards" {
					out[i].Yellow += int(value)
				} else {
					out[i].Red += int(value)
				}
			}
		}
	}
	return out
}
