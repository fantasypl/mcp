package algo

import (
	"context"

	"github.com/fantasypl/mcp/internal/fpl"
)

// stubClient serves deterministic, in-memory FPL payloads for tests and
// golden generation. Missing keyed values simulate HTTP 404 responses.
type stubClient struct {
	bootstrap       *fpl.Bootstrap
	fixtures        []fpl.Fixture
	picks           map[picksKey]*fpl.TeamPicks
	live            map[int]*fpl.LiveResponse
	eventStatus     *fpl.EventStatusResponse
	history         map[int]*fpl.TeamHistory
	leagues         map[int]*fpl.LeagueStandings
	transfers       map[int][]fpl.ManagerTransfer
	playerSummaries map[int]*fpl.PlayerSummary
}

type picksKey struct{ teamID, gw int }

// NewStubClient creates an in-memory client for offline algorithm runs.
func NewStubClient(bootstrap *fpl.Bootstrap, fixtures []fpl.Fixture) *stubClient {
	return &stubClient{bootstrap: bootstrap, fixtures: fixtures,
		picks: make(map[picksKey]*fpl.TeamPicks), live: make(map[int]*fpl.LiveResponse),
		history: make(map[int]*fpl.TeamHistory)}
}

func (s *stubClient) SetTeamPicks(teamID, gw int, v *fpl.TeamPicks) {
	s.picks[picksKey{teamID, gw}] = v
}
func (s *stubClient) SetLive(gw int, v *fpl.LiveResponse)       { s.live[gw] = v }
func (s *stubClient) SetEventStatus(v *fpl.EventStatusResponse) { s.eventStatus = v }
func (s *stubClient) SetHistory(teamID int, v *fpl.TeamHistory) { s.history[teamID] = v }
func (s *stubClient) SetLeague(leagueID int, v *fpl.LeagueStandings) {
	if s.leagues == nil {
		s.leagues = make(map[int]*fpl.LeagueStandings)
	}
	s.leagues[leagueID] = v
}
func (s *stubClient) SetTransfers(teamID int, v []fpl.ManagerTransfer) {
	if s.transfers == nil {
		s.transfers = make(map[int][]fpl.ManagerTransfer)
	}
	s.transfers[teamID] = v
}
func (s *stubClient) SetPlayerSummary(playerID int, v *fpl.PlayerSummary) {
	if s.playerSummaries == nil {
		s.playerSummaries = make(map[int]*fpl.PlayerSummary)
	}
	s.playerSummaries[playerID] = v
}

func (s *stubClient) Bootstrap(context.Context) (*fpl.Bootstrap, error) { return s.bootstrap, nil }
func (s *stubClient) Fixtures(context.Context) ([]fpl.Fixture, error)   { return s.fixtures, nil }
func (s *stubClient) TeamPicks(_ context.Context, teamID, gw int) (*fpl.TeamPicks, error) {
	p, ok := s.picks[picksKey{teamID, gw}]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return p, nil
}
func (s *stubClient) LivePoints(_ context.Context, gw int) (*fpl.LiveResponse, error) {
	if l, ok := s.live[gw]; ok {
		return l, nil
	}
	return &fpl.LiveResponse{}, nil
}
func (s *stubClient) EventStatus(context.Context) (*fpl.EventStatusResponse, error) {
	if s.eventStatus != nil {
		return s.eventStatus, nil
	}
	return &fpl.EventStatusResponse{}, nil
}
func (s *stubClient) TeamHistory(_ context.Context, teamID int) (*fpl.TeamHistory, error) {
	h, ok := s.history[teamID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return h, nil
}
func (s *stubClient) LeagueStandings(_ context.Context, leagueID int) (*fpl.LeagueStandings, error) {
	l, ok := s.leagues[leagueID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return l, nil
}
func (s *stubClient) ManagerTransfers(_ context.Context, teamID int) ([]fpl.ManagerTransfer, error) {
	t, ok := s.transfers[teamID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return t, nil
}
func (s *stubClient) PlayerSummary(_ context.Context, playerID int) (*fpl.PlayerSummary, error) {
	p, ok := s.playerSummaries[playerID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return p, nil
}
func (s *stubClient) ManagerStatus(ctx context.Context, teamID int, b *fpl.Bootstrap) (*fpl.ManagerStatus, error) {
	picks, err := s.TeamPicks(ctx, teamID, b.CurrentGameweek())
	if err != nil {
		return nil, err
	}
	history, err := s.TeamHistory(ctx, teamID)
	if err != nil {
		return nil, err
	}
	return fpl.DeriveManagerStatus(picks, history, b), nil
}
