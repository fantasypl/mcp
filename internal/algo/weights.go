package algo

// Weights for the captain model (v3.0).
//
// JSON tags match the persisted weight keys exactly, so
// data/optimized_weights.json remains readable. That file compatibility
// is a hard requirement — see the plan.
type Weights struct {
	XG90        float64 `json:"xg90"`
	XA90        float64 `json:"xa90"`
	Form        float64 `json:"form"`
	PPG         float64 `json:"ppg"`
	EPNext      float64 `json:"ep_next"`
	Home        float64 `json:"home"`
	FDR         float64 `json:"fdr"`
	ICT         float64 `json:"ict"`
	BonusPG     float64 `json:"bonus_pg"`
	Penalty     float64 `json:"penalty"`
	SetPiece    float64 `json:"set_piece"`
	Dreamteam   float64 `json:"dreamteam"`
	MinutesCert float64 `json:"minutes_cert"`
	DefContrib  float64 `json:"def_contrib"`
	NewsPenalty float64 `json:"news_penalty"`

	// PlayingChanceMaxPenalty is the flat penalty applied to a player who is
	// certain to miss; it scales linearly with chance_of_playing.
	PlayingChanceMaxPenalty float64 `json:"playing_chance_max_penalty"`
}

// weightFieldOrder lists every JSON key MergeWeights knows how to apply, so it
// doesn't need reflection to walk a Weights value.
var weightFieldOrder = []string{
	"xg90", "xa90", "form", "ppg", "ep_next", "home", "fdr", "ict",
	"bonus_pg", "penalty", "set_piece", "dreamteam", "minutes_cert",
	"def_contrib", "news_penalty", "playing_chance_max_penalty",
}

// MergeWeights overlays override onto base, one key at a time, leaving any key
// override doesn't mention at base's value.
//
// This exists because the weight optimizer's base set has one fewer key than
// the live scoring weights: it never touches news_penalty. The optimizer's
// base set omits that key, so the default value
// only ever fires when the active weight set came from the optimizer. Go's
// Weights has no notion of "key absent," so the same outcome is reproduced by
// merging onto DefaultWeights() (whose NewsPenalty is already 1.0) rather than
// onto a zero-valued struct, which would silently zero the term instead.
func MergeWeights(base Weights, override map[string]float64) Weights {
	m := base
	for _, key := range weightFieldOrder {
		v, ok := override[key]
		if !ok {
			continue
		}
		switch key {
		case "xg90":
			m.XG90 = v
		case "xa90":
			m.XA90 = v
		case "form":
			m.Form = v
		case "ppg":
			m.PPG = v
		case "ep_next":
			m.EPNext = v
		case "home":
			m.Home = v
		case "fdr":
			m.FDR = v
		case "ict":
			m.ICT = v
		case "bonus_pg":
			m.BonusPG = v
		case "penalty":
			m.Penalty = v
		case "set_piece":
			m.SetPiece = v
		case "dreamteam":
			m.Dreamteam = v
		case "minutes_cert":
			m.MinutesCert = v
		case "def_contrib":
			m.DefContrib = v
		case "news_penalty":
			m.NewsPenalty = v
		case "playing_chance_max_penalty":
			m.PlayingChanceMaxPenalty = v
		}
	}
	return m
}

// DefaultWeights are the hand-tuned v3.0 weights, tuned against GW1-29
// actuals via backtesting; changing one changes every recommendation.
func DefaultWeights() Weights {
	return Weights{
		XG90:        1.07,
		XA90:        0.92,
		Form:        3.43,
		PPG:         5.92,
		EPNext:      0.49,
		Home:        0.10,
		FDR:         0.30,
		ICT:         0.01,
		BonusPG:     1.31,
		Penalty:     1.90,
		SetPiece:    0.84,
		Dreamteam:   0.56,
		MinutesCert: 1.04,
		DefContrib:  0.59,
		NewsPenalty: 1.0,

		PlayingChanceMaxPenalty: -10.0,
	}
}
