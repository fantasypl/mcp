package fpl

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/fantasypl/mcp/internal/fplauth"
)

// fakeTokens hands out tok-1, tok-2, ... and counts invalidations.
type fakeTokens struct {
	n, invalidated int32
	err            error
}

func (f *fakeTokens) AccessToken(context.Context) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "tok-" + string(rune('0'+atomic.LoadInt32(&f.n)+1)), nil
}

func (f *fakeTokens) Invalidate() {
	atomic.AddInt32(&f.invalidated, 1)
	atomic.AddInt32(&f.n, 1)
}

func TestLoggedInCallsNeedALogin(t *testing.T) {
	var hits int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) })
	if c.LoggedIn() {
		t.Error("a new client should not be logged in")
	}
	if _, err := c.MyTeam(context.Background(), 1); !errors.Is(err, fplauth.ErrNotConfigured) {
		t.Errorf("MyTeam without login = %v, want ErrNotConfigured", err)
	}
	if hits != 0 {
		t.Errorf("made %d requests without a login, want 0", hits)
	}
}

func TestMyTeamSendsTokenAndDecodes(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/my-team/42/" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Api-Authorization"); got != "Bearer tok-1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"picks":[{"element":7,"position":1,"selling_price":71,"purchase_price":70,"multiplier":2,"is_captain":true,"is_vice_captain":false}],
			"chips":[{"name":"wildcard","number":1,"status_for_entry":"active","played_by_entry":[],"start_event":1,"stop_event":19,"chip_type":"transfer"}],
			"transfers":{"cost":4,"status":"unlimited","limit":null,"made":3,"bank":15,"value":1012}}`))
	})
	c.Auth = &fakeTokens{}
	mt, err := c.MyTeam(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(mt.Picks) != 1 || mt.Picks[0].SellingPrice != 71 || !mt.Picks[0].IsCaptain {
		t.Errorf("picks = %+v", mt.Picks)
	}
	if mt.Transfers.Limit != nil || mt.Transfers.Bank != 15 || mt.Chips[0].StatusForEntry != "active" {
		t.Errorf("transfers = %+v, chips = %+v", mt.Transfers, mt.Chips)
	}
}

func TestRejectedTokenIsRefreshedOnce(t *testing.T) {
	var hits int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("X-Api-Authorization") != "Bearer tok-2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"player":{"entry":99,"email":"x@example.com"}}`))
	})
	ft := &fakeTokens{}
	c.Auth = ft
	entry, err := c.MyEntry(context.Background())
	if err != nil || entry != 99 {
		t.Fatalf("MyEntry = %d, %v; want 99", entry, err)
	}
	if ft.invalidated != 1 || hits != 2 {
		t.Errorf("invalidated %d, requests %d; want 1 and 2", ft.invalidated, hits)
	}
}

func TestPersistentRejectionSaysLoginExpired(t *testing.T) {
	var hits int32
	c, slept, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
	})
	c.Auth = &fakeTokens{}
	_, err := c.MyTeam(context.Background(), 42)
	var ae *fplauth.Error
	if !errors.As(err, &ae) || ae.Msg != fplauth.Expired {
		t.Fatalf("got %v, want the expired-login error", err)
	}
	if hits != 2 || len(*slept) != 0 {
		t.Errorf("requests %d, sleeps %d; want 2 and no retry backoff", hits, len(*slept))
	}
}

func TestLoginErrorPassesThrough(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made when the token cannot be had")
	})
	want := &fplauth.Error{Msg: fplauth.Expired}
	c.Auth = &fakeTokens{err: want}
	if _, err := c.MyTeam(context.Background(), 1); !errors.Is(err, want) {
		t.Errorf("got %v, want the token source's error", err)
	}
}

func TestMyEntryWithoutTeam(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"player":null,"watched":[]}`))
	})
	c.Auth = &fakeTokens{}
	if _, err := c.MyEntry(context.Background()); err == nil {
		t.Error("MyEntry with no player should fail")
	}
}

func TestLoggedInRequestsDoNotFollowRedirects(t *testing.T) {
	var leaked int32
	other, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Authorization") != "" {
			atomic.AddInt32(&leaked, 1)
		}
	})
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.BaseURL+"/steal", http.StatusFound)
	})
	c.Auth = &fakeTokens{}
	if _, err := c.MyTeam(context.Background(), 1); err == nil {
		t.Error("a redirect should fail the request")
	}
	if leaked != 0 {
		t.Error("the token followed a redirect to another host")
	}
}
