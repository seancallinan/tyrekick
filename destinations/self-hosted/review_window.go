package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// isoInstantRE mirrors worker.ts's closedSince: ISO-8601 only, date-only or
// full instant, offset as "Z" or "+hh:mm"/"+hhmm". Date.parse alone (and
// time.Parse against a single Go layout) is too forgiving in the other
// direction, so the shape is checked here and the pieces parsed by hand.
var isoInstantRE = regexp.MustCompile(
	`^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?(Z|[+-]\d{2}:?\d{2}))?$`,
)

// parseISOInstant parses raw as an instant if (and only if) it matches the
// same ISO-8601 shape worker.ts accepts. Returns the zero time and false
// otherwise — never an error the caller has to reason about.
func parseISOInstant(raw string) (time.Time, bool) {
	m := isoInstantRE.FindStringSubmatch(raw)
	if m == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])

	// Date-only ("2026-08-28"): worker.ts hands this to `new Date(...)`,
	// which reads a bare date as UTC midnight.
	if m[4] == "" {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
	}

	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5])
	second := 0
	if m[6] != "" {
		second, _ = strconv.Atoi(m[6])
	}

	loc := time.UTC
	if off := m[7]; off != "Z" {
		sign := 1
		if off[0] == '-' {
			sign = -1
		}
		digits := strings.ReplaceAll(off[1:], ":", "")
		oh, _ := strconv.Atoi(digits[0:2])
		om, _ := strconv.Atoi(digits[2:4])
		loc = time.FixedZone(off, sign*(oh*3600+om*60))
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, loc), true
}

// closedSince returns the configured close instant, trimmed, once it has
// passed — or "" while the review is open. Absent, empty, whitespace-only
// and unparseable all read as "never closes": an operator typo can fail to
// close a review, but it can never silently shut a live one.
func closedSince(openUntil string) string {
	raw := strings.TrimSpace(openUntil)
	if raw == "" {
		return ""
	}
	t, ok := parseISOInstant(raw)
	if !ok {
		return ""
	}
	if !time.Now().Before(t) {
		return raw
	}
	return ""
}

// reviewState is the public window state echoed by /receipts: a var, not a
// secret, so this discloses nothing new.
func reviewState(openUntil string) map[string]interface{} {
	raw := strings.TrimSpace(openUntil)
	state := "open"
	if closedSince(openUntil) != "" {
		state = "closed"
	}
	var openUntilOut interface{}
	if raw != "" {
		openUntilOut = raw
	}
	return map[string]interface{}{"state": state, "open_until": openUntilOut}
}
