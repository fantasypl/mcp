package fpl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fantasypl/mcp/internal/fplauth"
)

// TokenSource supplies access tokens for FPL's logged-in endpoints.
// *fplauth.Source implements it.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
	Invalidate()
}

// MyTeamTTL is short because the squad, bank and chip state change the
// moment the manager confirms a transfer or chip in the FPL app.
const MyTeamTTL = 30 * time.Second

// authHeader is the header FPL's API reads the access token from.
const authHeader = "X-Api-Authorization"

// Me is GET /me/. Only the entry ID is decoded: the response also holds the
// account's name and email, which this server has no use for.
type Me struct {
	Player *struct {
		Entry int `json:"entry"`
	} `json:"player"`
}

// MyTeam is GET /my-team/{entry}/: the manager's squad as it stands in the
// FPL app, with pending transfers applied, exact purchase and selling
// prices, chip state and transfer budget. Prices are in tenths.
type MyTeam struct {
	Picks     []MyTeamPick    `json:"picks"`
	Chips     []MyTeamChip    `json:"chips"`
	Transfers MyTeamTransfers `json:"transfers"`
}

// MyTeamPick is one squad slot.
type MyTeamPick struct {
	Element       int  `json:"element"`
	Position      int  `json:"position"`
	SellingPrice  int  `json:"selling_price"`
	PurchasePrice int  `json:"purchase_price"`
	Multiplier    int  `json:"multiplier"`
	IsCaptain     bool `json:"is_captain"`
	IsViceCaptain bool `json:"is_vice_captain"`
}

// MyTeamChip is one chip and whether this manager can still play it.
type MyTeamChip struct {
	Name           string `json:"name"`
	Number         int    `json:"number"`
	StatusForEntry string `json:"status_for_entry"`
	PlayedByEntry  []int  `json:"played_by_entry"`
	StartEvent     int    `json:"start_event"`
	StopEvent      int    `json:"stop_event"`
	ChipType       string `json:"chip_type"`
}

// MyTeamTransfers is the manager's transfer state for the next deadline.
// Limit is nil when transfers are unlimited, as on a Wildcard or Free Hit.
type MyTeamTransfers struct {
	Cost   int    `json:"cost"`
	Status string `json:"status"`
	Limit  *int   `json:"limit"`
	Made   int    `json:"made"`
	Bank   int    `json:"bank"`
	Value  int    `json:"value"`
}

// LoggedIn reports whether the client has a login to use.
func (c *Client) LoggedIn() bool { return c.Auth != nil }

// fetchAuthed is fetch for a logged-in endpoint. If FPL rejects the access
// token it drops it, gets a fresh one and tries once more.
func fetchAuthed[T any](ctx context.Context, c *Client, path string, ttl time.Duration) (T, error) {
	var zero T
	if c.Auth == nil {
		return zero, fplauth.ErrNotConfigured
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.Auth.AccessToken(ctx)
		if err != nil {
			return zero, err
		}
		out, err := fetchWith[T](ctx, c, path, ttl, http.Header{authHeader: {"Bearer " + tok}})
		var he *HTTPError
		if errors.As(err, &he) && (he.StatusCode == http.StatusUnauthorized || he.StatusCode == http.StatusForbidden) {
			c.Auth.Invalidate()
			continue
		}
		return out, err
	}
	return zero, &fplauth.Error{Msg: fplauth.Expired}
}

// MyEntry returns the entry (team) ID of the logged-in account.
func (c *Client) MyEntry(ctx context.Context) (int, error) {
	me, err := fetchAuthed[*Me](ctx, c, "/me/", DefaultTTL)
	if err != nil {
		return 0, err
	}
	if me == nil || me.Player == nil || me.Player.Entry == 0 {
		return 0, &fplauth.Error{Msg: "the FPL login has no team. Check that the account you logged in with has entered FPL this season"}
	}
	return me.Player.Entry, nil
}

// MyTeam returns GET /my-team/{entry}/. FPL serves it only for the
// logged-in account's own entry.
func (c *Client) MyTeam(ctx context.Context, entry int) (*MyTeam, error) {
	return fetchAuthed[*MyTeam](ctx, c, fmt.Sprintf("/my-team/%d/", entry), MyTeamTTL)
}

// TransfersLatest returns GET /entry/{entry}/transfers-latest/: the
// transfers made since the last deadline, which the public transfer history
// leaves out until the deadline passes.
func (c *Client) TransfersLatest(ctx context.Context, entry int) ([]ManagerTransfer, error) {
	return fetchAuthed[[]ManagerTransfer](ctx, c, fmt.Sprintf("/entry/%d/transfers-latest/", entry), MyTeamTTL)
}
