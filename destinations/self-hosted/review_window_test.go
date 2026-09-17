package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

/* ------------------------------------------------------------------------ */
/* closedSince / reviewState — pure logic                                   */
/* ------------------------------------------------------------------------ */

func TestClosedSinceFailsOpenForEveryUnusableValue(t *testing.T) {
	for _, raw := range []string{"", "   ", "not-a-date", "next friday", "2026-9-15", "15/09/2026"} {
		if got := closedSince(raw); got != "" {
			t.Errorf("closedSince(%q) = %q, want open (\"\")", raw, got)
		}
	}
}

func TestClosedSinceAFutureInstantIsOpen(t *testing.T) {
	if got := closedSince("2099-01-01T00:00:00.000Z"); got != "" {
		t.Errorf("closedSince(future) = %q, want open", got)
	}
}

func TestClosedSinceAPastInstantIsClosed(t *testing.T) {
	if got := closedSince("2026-08-28T00:00:00.000Z"); got != "2026-08-28T00:00:00.000Z" {
		t.Errorf("closedSince(past) = %q", got)
	}
}

func TestClosedSinceBoundaryIsInclusive(t *testing.T) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if got := closedSince(now); got != now {
		t.Errorf("closedSince(now) = %q, want closed (an instant equal to now is closed)", got)
	}
}

func TestReviewStateShape(t *testing.T) {
	if got := reviewState(""); got["state"] != "open" || got["open_until"] != nil {
		t.Errorf("reviewState(\"\") = %#v", got)
	}
	future := "2099-01-01T00:00:00.000Z"
	if got := reviewState(future); got["state"] != "open" || got["open_until"] != future {
		t.Errorf("reviewState(future) = %#v", got)
	}
	past := "2026-08-28T00:00:00.000Z"
	if got := reviewState(past); got["state"] != "closed" || got["open_until"] != past {
		t.Errorf("reviewState(past) = %#v", got)
	}
}

/* ------------------------------------------------------------------------ */
/* Ingest gate                                                               */
/* ------------------------------------------------------------------------ */

const pastWindow = "2026-08-28T00:00:00.000Z"
const futureWindow = "2099-01-01T00:00:00.000Z"

// THE regression test: an app with no TYREKICK_OPEN_UNTIL must keep accepting
// comments exactly as before this feature existed.
func TestIngestWithNoWindowConfiguredWorksAsBefore(t *testing.T) {
	app := newTestApp(t, nil)
	w, got := do(t, app, http.MethodPost, "/feedback", `{"schema":2,"body":"hi","project_name":"p","route":"/"}`, nil)
	if w.Code != http.StatusOK || got["ok"] != true {
		t.Fatalf("ingest with no window: %d %v", w.Code, got)
	}
}

func TestIngestAFutureWindowLeavesTheReviewOpen(t *testing.T) {
	app := newTestApp(t, func(c *Config) { c.OpenUntil = futureWindow })
	w, got := do(t, app, http.MethodPost, "/feedback", `{"schema":2,"body":"hi","project_name":"p","route":"/"}`, nil)
	if w.Code != http.StatusOK || got["ok"] != true {
		t.Fatalf("ingest with future window: %d %v", w.Code, got)
	}
}

func TestIngestAPastWindowRefusesWithReviewClosed(t *testing.T) {
	discordHits := 0
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discordHits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer discord.Close()

	app := newTestApp(t, func(c *Config) {
		c.OpenUntil = pastWindow
		c.DiscordWebhook = discord.URL
	})
	w, got := do(t, app, http.MethodPost, "/feedback", `{"schema":2,"body":"hi","project_name":"p","route":"/"}`, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if got["ok"] != false || got["error"] != "review_closed" || got["open_until"] != pastWindow {
		t.Fatalf("body = %v", got)
	}
	app.waitForBackground(time.Second)
	if discordHits != 0 {
		t.Errorf("discordHits = %d, want 0 — a closed review must tee nothing", discordHits)
	}
}

func TestIngestAPastWindowRefusalCarriesCORS(t *testing.T) {
	app := newTestApp(t, func(c *Config) { c.OpenUntil = pastWindow })
	w, _ := do(t, app, http.MethodPost, "/feedback", `{"schema":2,"body":"hi"}`, nil)
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("missing CORS header on the 403")
	}
}

func TestIngestAPastWindowAlsoGatesTheRootRoute(t *testing.T) {
	app := newTestApp(t, func(c *Config) { c.OpenUntil = pastWindow })
	w, got := do(t, app, http.MethodPost, "/", `{"schema":2,"body":"hi"}`, nil)
	if w.Code != http.StatusForbidden || got["error"] != "review_closed" {
		t.Fatalf("POST / with closed window: %d %v", w.Code, got)
	}
}

/* ------------------------------------------------------------------------ */
/* Every read route still works while closed                                */
/* ------------------------------------------------------------------------ */

func TestReadRoutesStillWorkWhileTheWindowIsClosed(t *testing.T) {
	app := newTestApp(t, func(c *Config) { c.ReviewKey = "rk-secret" })
	id := ingest(t, app, map[string]interface{}{"project_name": "demo"})
	// Close the window only AFTER the comment is in, so the write path above
	// doesn't need to be exercised twice.
	app.cfg.OpenUntil = pastWindow

	if w, got := do(t, app, http.MethodGet, "/feedback", "", authHeader()); w.Code != http.StatusOK || got["ok"] != true {
		t.Errorf("GET /feedback while closed: %d %v", w.Code, got)
	}
	if w, got := do(t, app, http.MethodGet, "/feedback/"+id, "", authHeader()); w.Code != http.StatusOK || got["ok"] != true {
		t.Errorf("GET /feedback/:id while closed: %d %v", w.Code, got)
	}
	if w, got := do(t, app, http.MethodPatch, "/feedback/"+id, `{"status":"resolved","note":"fixed"}`, authHeader()); w.Code != http.StatusOK || got["ok"] != true {
		t.Errorf("PATCH /feedback/:id while closed: %d %v", w.Code, got)
	}
	if w, got := do(t, app, http.MethodGet, "/receipts?ids="+id, "", nil); w.Code != http.StatusOK || got["ok"] != true {
		t.Errorf("GET /receipts while closed: %d %v", w.Code, got)
	}
	headers := map[string]string{"X-Tyrekick-Review-Key": "rk-secret"}
	if w, got := do(t, app, http.MethodGet, "/shared?project=demo", "", headers); w.Code != http.StatusOK || got["ok"] != true {
		t.Errorf("GET /shared while closed: %d %v", w.Code, got)
	}
}

/* ------------------------------------------------------------------------ */
/* /receipts discovery envelope                                              */
/* ------------------------------------------------------------------------ */

func TestReceiptsCarryTheReviewEnvelope(t *testing.T) {
	app := newTestApp(t, nil)
	w, got := do(t, app, http.MethodGet, "/receipts?ids=", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	review, _ := got["review"].(map[string]interface{})
	if review["state"] != "open" || review["open_until"] != nil {
		t.Errorf("no var -> %#v, want open/nil", review)
	}

	app2 := newTestApp(t, func(c *Config) { c.OpenUntil = futureWindow })
	_, got2 := do(t, app2, http.MethodGet, "/receipts?ids=", "", nil)
	review2, _ := got2["review"].(map[string]interface{})
	if review2["state"] != "open" || review2["open_until"] != futureWindow {
		t.Errorf("future var -> %#v", review2)
	}

	app3 := newTestApp(t, func(c *Config) { c.OpenUntil = pastWindow })
	_, got3 := do(t, app3, http.MethodGet, "/receipts?ids=", "", nil)
	review3, _ := got3["review"].(map[string]interface{})
	if review3["state"] != "closed" || review3["open_until"] != pastWindow {
		t.Errorf("past var -> %#v", review3)
	}
}

func TestReceiptsPopulatedBranchAlsoCarriesReviewAndProjectionIsUnchanged(t *testing.T) {
	app := newTestApp(t, nil)
	id := ingest(t, app, nil)
	do(t, app, http.MethodPatch, "/feedback/"+id, `{"status":"resolved","note":"done"}`, authHeader())
	app.cfg.OpenUntil = pastWindow

	w, got := do(t, app, http.MethodGet, "/receipts?ids="+id, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	review, _ := got["review"].(map[string]interface{})
	if review["state"] != "closed" || review["open_until"] != pastWindow {
		t.Errorf("review = %#v", review)
	}
	receipts, _ := got["receipts"].([]interface{})
	if len(receipts) != 1 {
		t.Fatalf("receipts = %v", receipts)
	}
	r := receipts[0].(map[string]interface{})
	if r["id"] != id || r["status"] != "resolved" || r["resolution_note"] != "done" {
		t.Errorf("receipt = %#v", r)
	}
}

func TestRootRouteCarriesNoReviewKey(t *testing.T) {
	app := newTestApp(t, func(c *Config) { c.OpenUntil = pastWindow })
	_, got := do(t, app, http.MethodGet, "/", "", nil)
	if _, ok := got["review"]; ok {
		t.Errorf("GET / must not gain a review key: %#v", got)
	}
}
