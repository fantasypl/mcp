package algo

import (
	"context"

	"github.com/fantasypl/mcp/internal/fpl"
)

// StubClient serves deterministic, in-memory FPL payloads for tests and
// golden generation. Missing keyed values simulate HTTP 404 responses.
type StubClient struct {
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
func NewStubClient(bootstrap *fpl.Bootstrap, fixtures []fpl.Fixture) *StubClient {
	return &StubClient{bootstrap: bootstrap, fixtures: fixtures,
		picks: make(map[picksKey]*fpl.TeamPicks), live: make(map[int]*fpl.LiveResponse),
		history: make(map[int]*fpl.TeamHistory)}
}

func (s *StubClient) SetTeamPicks(teamID, gw int, v *fpl.TeamPicks) {
	s.picks[picksKey{teamID, gw}] = v
}
func (s *StubClient) SetLive(gw int, v *fpl.LiveResponse)       { s.live[gw] = v }
func (s *StubClient) SetEventStatus(v *fpl.EventStatusResponse) { s.eventStatus = v }
func (s *StubClient) SetHistory(teamID int, v *fpl.TeamHistory) { s.history[teamID] = v }
func (s *StubClient) SetLeague(leagueID int, v *fpl.LeagueStandings) {
	if s.leagues == nil {
		s.leagues = make(map[int]*fpl.LeagueStandings)
	}
	s.leagues[leagueID] = v
}
func (s *StubClient) SetTransfers(teamID int, v []fpl.ManagerTransfer) {
	if s.transfers == nil {
		s.transfers = make(map[int][]fpl.ManagerTransfer)
	}
	s.transfers[teamID] = v
}
func (s *StubClient) SetPlayerSummary(playerID int, v *fpl.PlayerSummary) {
	if s.playerSummaries == nil {
		s.playerSummaries = make(map[int]*fpl.PlayerSummary)
	}
	s.playerSummaries[playerID] = v
}

func (s *StubClient) Bootstrap(context.Context) (*fpl.Bootstrap, error) { return s.bootstrap, nil }
func (s *StubClient) Fixtures(context.Context) ([]fpl.Fixture, error)   { return s.fixtures, nil }
func (s *StubClient) TeamPicks(_ context.Context, teamID, gw int) (*fpl.TeamPicks, error) {
	p, ok := s.picks[picksKey{teamID, gw}]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return p, nil
}
func (s *StubClient) LivePoints(_ context.Context, gw int) (*fpl.LiveResponse, error) {
	if l, ok := s.live[gw]; ok {
		return l, nil
	}
	return &fpl.LiveResponse{}, nil
}
func (s *StubClient) EventStatus(context.Context) (*fpl.EventStatusResponse, error) {
	if s.eventStatus != nil {
		return s.eventStatus, nil
	}
	return &fpl.EventStatusResponse{}, nil
}
func (s *StubClient) TeamHistory(_ context.Context, teamID int) (*fpl.TeamHistory, error) {
	h, ok := s.history[teamID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return h, nil
}
func (s *StubClient) LeagueStandings(_ context.Context, leagueID int) (*fpl.LeagueStandings, error) {
	l, ok := s.leagues[leagueID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return l, nil
}
func (s *StubClient) ManagerTransfers(_ context.Context, teamID int) ([]fpl.ManagerTransfer, error) {
	t, ok := s.transfers[teamID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return t, nil
}
func (s *StubClient) PlayerSummary(_ context.Context, playerID int) (*fpl.PlayerSummary, error) {
	p, ok := s.playerSummaries[playerID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return p, nil
}
func (s *StubClient) ManagerStatus(ctx context.Context, teamID int, b *fpl.Bootstrap) (*fpl.ManagerStatus, error) {
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
