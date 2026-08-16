package algo

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// Double and blank gameweeks are the single biggest wrinkle in season planning
// — a team playing twice (or not at all) can be worth more to a squad than any
// single-match form guide — and the FPL API only reflects them once fixtures
// are officially rescheduled, which is often weeks after pundits and clubs
// already know. This file extracts DGW/BGW predictions from a couple of
// trusted community sources and merges them with what the API already knows,
// so a chip-timing recommendation can react before the API catches up.
//
// The extraction is deliberately conservative: regex over plain text, no HTML
// parsing beyond stripping tags, and every prediction is labelled "predicted"
// unless the source text itself uses confirming language. A source going
// offline or changing its layout degrades this to zero findings, not an
// error — see FetchCommunityDGWIntel.

// intelSource is one page scraped for DGW/BGW mentions.
type intelSource struct {
	Name string
	URL  string
	// Official sources upgrade a prediction straight to "confirmed" even if
	// the sentence itself doesn't use confirming language.
	Official bool
}

var intelSources = []intelSource{
	{
		Name:     "premierleague.com",
		URL:      "https://www.premierleague.com/en/news/4611210/what-we-know-so-far-about-blank-and-double-gameweeks-this-season",
		Official: true,
	},
	{
		Name: "allaboutfpl.com",
		URL:  "https://allaboutfpl.com/2026/01/upcoming-fpl-double-blank-gameweeks-25-26-fpl-season/",
	},
}

// teamAliasGroups lists every name a source is likely to use for each club,
// short_name first for readability; order otherwise reflects how a human
// would write about a club, longest names last so substring matching below
// prefers the more specific alias.
var teamAliasGroups = []struct {
	Short   string
	Aliases []string
}{
	{"ARS", []string{"arsenal", "ars"}},
	{"AVL", []string{"aston villa", "villa", "avl"}},
	{"BOU", []string{"bournemouth", "bou"}},
	{"BRE", []string{"brentford", "bre"}},
	{"BHA", []string{"brighton", "bha", "brighton and hove"}},
	{"CHE", []string{"chelsea", "che"}},
	{"CRY", []string{"crystal palace", "palace", "cry"}},
	{"EVE", []string{"everton", "eve"}},
	{"FUL", []string{"fulham", "ful"}},
	{"IPS", []string{"ipswich", "ips"}},
	{"LEI", []string{"leicester", "lei"}},
	{"LIV", []string{"liverpool", "liv"}},
	{"MCI", []string{"manchester city", "man city", "mci", "city"}},
	{"MUN", []string{"manchester united", "man united", "man utd", "mun", "united"}},
	{"NEW", []string{"newcastle", "new", "newcastle united"}},
	{"NFO", []string{"nottingham forest", "nfo", "forest", "nott'm forest"}},
	{"SOU", []string{"southampton", "sou"}},
	{"SUN", []string{"sunderland", "sun"}},
	{"TOT", []string{"tottenham", "spurs", "tot"}},
	{"WHU", []string{"west ham", "whu", "west ham united"}},
	{"WOL", []string{"wolves", "wol", "wolverhampton"}},
	{"LEE", []string{"leeds", "lee", "leeds united"}},
}

// aliasEntry is one (alias, short_name) pair in a fixed, deterministic order —
// needed because MatchTeamName's substring fallback returns on first hit, so
// iteration order decides the result when a snippet could match more than one
// club.
type aliasEntry struct {
	alias string
	short string
}

var aliasOrder = buildAliasOrder()

func buildAliasOrder() []aliasEntry {
	var out []aliasEntry
	for _, g := range teamAliasGroups {
		for _, a := range g.Aliases {
			out = append(out, aliasEntry{alias: a, short: g.Short})
		}
	}
	return out
}

// MatchTeamName resolves free text to an FPL team short_name, trying an exact
// match before falling back to "does this alias appear anywhere in the text".
func MatchTeamName(text string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, e := range aliasOrder {
		if e.alias == lower {
			return e.short, true
		}
	}
	for _, e := range aliasOrder {
		if strings.Contains(lower, e.alias) {
			return e.short, true
		}
	}
	return "", false
}

var (
	dgwPattern = regexp.MustCompile(`(?i)(?:double\s*(?:game\s*week|gw)\s*(\d{1,2})|dgw\s*(\d{1,2}))`)
	bgwPattern = regexp.MustCompile(`(?i)(?:blank\s*(?:game\s*week|gw)\s*(\d{1,2})|bgw\s*(\d{1,2}))`)
)

var (
	dgwConfirmWords = []string{"confirmed", "official", "scheduled", "will play"}
	bgwConfirmWords = []string{"confirmed", "official", "will not play", "will blank"}
)

// GWMention is one gameweek's DGW or BGW prediction as extracted from a single
// piece of text, before merging across sources.
type GWMention struct {
	Teams  []string `json:"teams"`
	Status string   `json:"status"` // "predicted" or "confirmed"
}

// TextIntel is what ExtractDGWBGWFromText finds in one document, keyed by
// gameweek number as a string to match the shape callers pass over JSON.
type TextIntel struct {
	DGWs map[string]GWMention `json:"dgws"`
	BGWs map[string]GWMention `json:"bgws"`
}

func newTextIntel() TextIntel {
	return TextIntel{DGWs: map[string]GWMention{}, BGWs: map[string]GWMention{}}
}

// ExtractDGWBGWFromText scans article text for "Double Gameweek 33" / "DGW33"
// / "DGW 33" style mentions (and the Blank Gameweek equivalents), pulls the
// team names mentioned nearby, and grades each mention "confirmed" if the
// surrounding sentence uses confirming language.
func ExtractDGWBGWFromText(text string) TextIntel {
	result := newTextIntel()
	normalized := strings.NewReplacer("\n", " ", "\r", " ").Replace(text)

	scan := func(pattern *regexp.Regexp, confirmWords []string, dst map[string]GWMention) {
		for _, loc := range pattern.FindAllStringSubmatchIndex(normalized, -1) {
			gw, ok := gwFromMatch(normalized, loc)
			if !ok || gw < 1 || gw > 38 {
				continue
			}

			start := max(0, loc[0]-100)
			end := min(len(normalized), loc[1]+200)
			context := normalized[start:end]
			contextLower := strings.ToLower(context)

			teams := map[string]bool{}
			for _, g := range teamAliasGroups {
				for _, alias := range g.Aliases {
					if wordBoundaryContains(contextLower, alias) {
						teams[g.Short] = true
						break
					}
				}
			}

			status := "predicted"
			for _, w := range confirmWords {
				if strings.Contains(contextLower, w) {
					status = "confirmed"
					break
				}
			}

			key := strconv.Itoa(gw)
			existing, ok := dst[key]
			if !ok {
				existing = GWMention{Status: "predicted"}
			}
			existing.Teams = mergeSortedSet(existing.Teams, setKeys(teams))
			if status == "confirmed" {
				existing.Status = "confirmed"
			}
			dst[key] = existing
		}
	}

	scan(dgwPattern, dgwConfirmWords, result.DGWs)
	scan(bgwPattern, bgwConfirmWords, result.BGWs)
	return result
}

// gwFromMatch extracts whichever of the pattern's two numbered capture groups
// matched — the patterns are (long form | short form), so exactly one fires.
func gwFromMatch(s string, loc []int) (int, bool) {
	for _, pair := range [][2]int{{2, 3}, {4, 5}} {
		lo, hi := loc[pair[0]], loc[pair[1]]
		if lo < 0 || hi < 0 {
			continue
		}
		n, err := strconv.Atoi(s[lo:hi])
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// wordBoundaryContains reports whether alias appears in s as a whole word —
// not as a substring of a longer word — mirroring Python's `\bALIAS\b`.
func wordBoundaryContains(s, alias string) bool {
	if alias == "" {
		return false
	}
	idx := 0
	for {
		i := strings.Index(s[idx:], alias)
		if i < 0 {
			return false
		}
		pos := idx + i
		before := byte(' ')
		if pos > 0 {
			before = s[pos-1]
		}
		after := byte(' ')
		if end := pos + len(alias); end < len(s) {
			after = s[end]
		}
		if !isWordByte(before) && !isWordByte(after) {
			return true
		}
		idx = pos + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mergeSortedSet(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		set[x] = true
	}
	return setKeys(set)
}

// CommunityIntel is the merged result of scraping every source: per-gameweek
// predictions plus which sources were consulted and which failed.
type CommunityIntel struct {
	DGWs           map[string]SourcedMention `json:"dgws"`
	BGWs           map[string]SourcedMention `json:"bgws"`
	SourcesChecked []string                  `json:"sources_checked"`
	Errors         []string                  `json:"errors"`
}

// SourcedMention is a GWMention plus which sources contributed to it.
type SourcedMention struct {
	Teams   []string `json:"teams"`
	Status  string   `json:"status"`
	Sources []string `json:"sources"`
}

const intelCacheTTL = time.Hour

// scraperUserAgent identifies requests to the community sources. Distinct from
// the FPL client's User-Agent since these are unrelated hosts with their own
// bot-detection behaviour.
const scraperUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// DGWIntelFetcher fetches and caches community DGW/BGW predictions. The zero
// value is unusable; construct with NewDGWIntelFetcher.
//
// A struct rather than a bare function because the result is cached for an
// hour — these sources do not change minute to minute, and hammering them on
// every chip_strategy call would be both slow and a poor way to treat a site
// that owes us nothing.
type DGWIntelFetcher struct {
	http    *http.Client
	sources []intelSource

	mu      sync.Mutex
	cached  *CommunityIntel
	expires time.Time
	now     func() time.Time
}

func NewDGWIntelFetcher() *DGWIntelFetcher {
	return &DGWIntelFetcher{
		http:    &http.Client{Timeout: 10 * time.Second},
		sources: intelSources,
		now:     time.Now,
	}
}

var (
	scriptOrStyleTag = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	anyTag           = regexp.MustCompile(`<[^>]+>`)
	whitespaceRun    = regexp.MustCompile(`\s+`)
)

func stripHTML(html string) string {
	text := scriptOrStyleTag.ReplaceAllString(html, " ")
	text = anyTag.ReplaceAllString(text, " ")
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(text, " "))
}

func (f *DGWIntelFetcher) fetchArticle(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", scraperUserAgent)

	resp, err := f.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &HTTPStatusError{URL: url, StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return "", err
	}
	return stripHTML(string(body)), nil
}

// HTTPStatusError is a non-2xx response fetching a community source.
type HTTPStatusError struct {
	URL        string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return e.URL + ": HTTP " + strconv.Itoa(e.StatusCode)
}

// Fetch scrapes every configured source and merges the results, returning a
// cached copy if the last fetch is under an hour old.
//
// A single source failing does not fail the call — it is recorded in Errors
// and the remaining sources still contribute. This mirrors how the caller
// (chip strategy) treats the whole feature as best-effort: see chips.go,
// which falls back to API-only DGW detection if this returns nothing useful.
func (f *DGWIntelFetcher) Fetch(ctx context.Context) (*CommunityIntel, error) {
	f.mu.Lock()
	if f.cached != nil && f.now().Before(f.expires) {
		cached := f.cached
		f.mu.Unlock()
		return cached, nil
	}
	f.mu.Unlock()

	merged := CommunityIntel{
		DGWs: map[string]SourcedMention{},
		BGWs: map[string]SourcedMention{},
	}

	for _, src := range f.sources {
		merged.SourcesChecked = append(merged.SourcesChecked, src.Name)

		text, err := f.fetchArticle(ctx, src.URL)
		if err != nil {
			merged.Errors = append(merged.Errors, src.Name+": "+err.Error())
			continue
		}

		parsed := ExtractDGWBGWFromText(text)
		mergeInto(merged.DGWs, parsed.DGWs, src)
		mergeInto(merged.BGWs, parsed.BGWs, src)
	}

	f.mu.Lock()
	f.cached = &merged
	f.expires = f.now().Add(intelCacheTTL)
	f.mu.Unlock()

	return &merged, nil
}

func mergeInto(dst map[string]SourcedMention, src map[string]GWMention, source intelSource) {
	for gw, info := range src {
		existing, ok := dst[gw]
		if !ok {
			existing = SourcedMention{Status: "predicted"}
		}
		existing.Teams = mergeSortedSet(existing.Teams, info.Teams)
		existing.Sources = append(existing.Sources, source.Name)
		if info.Status == "confirmed" || source.Official {
			existing.Status = "confirmed"
		}
		dst[gw] = existing
	}
}

// MergeIntelWithAPIPredictions folds community DGW predictions into the API's
// own event=null-derived predictions, returning a new map keyed by gameweek
// number rather than mutating apiPredictions.
//
// Community intel is keyed by team short_name; API predictions are keyed by
// team ID, so this also does that translation. A short_name with no matching
// team is silently dropped rather than erroring — a scraped page can mention
// a name that doesn't resolve, and that should not break the merge for every
// other prediction in the same document.
func MergeIntelWithAPIPredictions(apiPredictions map[int][]int, intel *CommunityIntel, teams map[int]*fpl.Team) map[int][]int {
	shortToID := make(map[string]int, len(teams))
	for id, t := range teams {
		if t != nil {
			shortToID[t.ShortName] = id
		}
	}

	merged := make(map[int][]int, len(apiPredictions))
	for gw, ids := range apiPredictions {
		merged[gw] = append([]int(nil), ids...)
	}

	if intel == nil {
		return merged
	}

	for gwStr, mention := range intel.DGWs {
		gw, err := strconv.Atoi(gwStr)
		if err != nil {
			continue
		}
		existing := map[int]bool{}
		for _, id := range merged[gw] {
			existing[id] = true
		}
		for _, short := range mention.Teams {
			if id, ok := shortToID[short]; ok {
				existing[id] = true
			}
		}
		if len(existing) > 0 {
			ids := make([]int, 0, len(existing))
			for id := range existing {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			merged[gw] = ids
		}
	}

	return merged
}
