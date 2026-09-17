package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Connectivity checks (`tyrekick init`, the make-reviewable skill) must not
// look like feedback. Stored as "open", they sat in an open queue forever —
// mirrors test/unit/verification-kind.test.ts on the Cloudflare worker.

func TestVerificationKindIsStoredAlreadyResolvedWithANote(t *testing.T) {
	app := newTestApp(t, nil)
	id := ingest(t, app, map[string]interface{}{"kind": "verification"})

	stored, err := app.store.LoadRecord(id)
	if err != nil || stored == nil {
		t.Fatalf("LoadRecord(%q): %v", id, err)
	}
	if stored.status() != string(StatusResolved) {
		t.Errorf("status = %q, want resolved", stored.status())
	}
	if stored["resolved_at"] != stored["received_at"] {
		t.Errorf("resolved_at = %v, want it to equal received_at (%v)", stored["resolved_at"], stored["received_at"])
	}
	note, _ := stored["resolution_note"].(string)
	if note == "" {
		t.Error("resolution_note is empty, want a connectivity-check explanation")
	}
}

func TestVerificationKindDoesNotAppearInTheOpenList(t *testing.T) {
	app := newTestApp(t, nil)
	ingest(t, app, map[string]interface{}{"kind": "verification"})

	w, got := do(t, app, http.MethodGet, "/feedback?status=open", "", authHeader())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	items, _ := got["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("open list = %v, want empty", items)
	}
}

func TestVerificationKindStillMirrorsToDiscord(t *testing.T) {
	hits := 0
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer discord.Close()

	app := newTestApp(t, func(c *Config) { c.DiscordWebhook = discord.URL })
	ingest(t, app, map[string]interface{}{"kind": "verification"})
	app.waitForBackground(time.Second)

	if hits != 1 {
		t.Errorf("discord hits = %d, want 1 — the check exists to prove that path works", hits)
	}
}

func TestVerificationKindDoesNotSpendAnAIReplyOnItself(t *testing.T) {
	app := newTestApp(t, func(c *Config) { c.AnthropicAPIKey = "test-key"; c.AIDailyCap = 1 })
	ingest(t, app, map[string]interface{}{"kind": "verification"})
	app.waitForBackground(time.Second)

	// If maybeReply had run, it would have reserved today's one-slot budget
	// before ever making a network call. An untouched budget proves it was
	// never invoked for this record.
	day := time.Now().UTC().Format("2006-01-02")
	if !app.store.ReserveAIReply(day, 1) {
		t.Error("AI budget was already spent — maybeReply ran for a verification comment")
	}
}

// Back-compat: every browser payload, and every client older than this field.
func TestAPayloadWithNoKindIsStillStoredOpen(t *testing.T) {
	app := newTestApp(t, nil)
	id := ingest(t, app, nil)
	stored, _ := app.store.LoadRecord(id)
	if stored.status() != string(StatusOpen) {
		t.Errorf("status = %q, want open", stored.status())
	}
	if stored["resolved_at"] != nil || stored["resolution_note"] != nil {
		t.Errorf("resolved_at/resolution_note = %v/%v, want both null", stored["resolved_at"], stored["resolution_note"])
	}
}

func TestAnUnrecognisedKindIsTreatedAsARealComment(t *testing.T) {
	app := newTestApp(t, nil)
	id := ingest(t, app, map[string]interface{}{"kind": "something-else"})
	stored, _ := app.store.LoadRecord(id)
	if stored.status() != string(StatusOpen) {
		t.Errorf("status = %q, want open", stored.status())
	}
}
