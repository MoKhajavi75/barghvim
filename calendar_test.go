package main

import (
	"strings"
	"testing"
	"time"
)

func TestBuildICS(t *testing.T) {
	loc := tehran(t)

	outages := []Outage{{
		Start: time.Date(2025, 9, 7, 9, 0, 0, 0, loc),
		End:   time.Date(2025, 9, 7, 11, 0, 0, 0, loc),
	}}

	feed, err := buildICS("1234567890", outages, loc)
	if err != nil {
		t.Fatalf("buildICS() = %v", err)
	}
	got := string(feed)

	// Tehran is UTC+03:30, and the library serializes DTSTART/DTEND as UTC.
	want := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"METHOD:PUBLISH",
		"PRODID:" + productID,
		"X-WR-CALNAME:Power Outages – 1234567890",
		"X-WR-TIMEZONE:Asia/Tehran",
		"BEGIN:VEVENT",
		"DTSTART:20250907T053000Z",
		"DTEND:20250907T073000Z",
		"DTSTAMP:20250907T053000Z",
		"TRANSP:TRANSPARENT",
		"END:VEVENT",
		"END:VCALENDAR",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("feed is missing %q\n%s", w, got)
		}
	}
}

// TestBuildICSOmitsClass pins the bug that kept the feed invisible in Google
// Calendar. Google fetches a feed whose events carry CLASS:PRIVATE, answers
// 200, and then renders none of them; Apple Calendar displays them, so the
// feed appears healthy everywhere the author is likely to check.
func TestBuildICSOmitsClass(t *testing.T) {
	loc := tehran(t)

	start := time.Date(2025, 9, 7, 9, 0, 0, 0, loc)
	outages := []Outage{{Start: start, End: start.Add(2 * time.Hour)}}

	feed, err := buildICS("1234567890", outages, loc)
	if err != nil {
		t.Fatalf("buildICS() = %v", err)
	}

	if got := string(feed); strings.Contains(got, "CLASS:") {
		t.Errorf("feed sets CLASS; Google Calendar drops such events from a subscription:\n%s", got)
	}
}

func TestBuildICSEmpty(t *testing.T) {
	loc := tehran(t)

	feed, err := buildICS("1234567890", nil, loc)
	if err != nil {
		t.Fatalf("buildICS() = %v", err)
	}

	// An empty feed is still a valid calendar; clients treat it as "no
	// outages" rather than as an error.
	if got := string(feed); strings.Contains(got, "BEGIN:VEVENT") {
		t.Errorf("feed for no outages contains an event:\n%s", got)
	}
}

func TestBuildICSIsDeterministic(t *testing.T) {
	loc := tehran(t)

	outages := []Outage{{
		Start: time.Date(2025, 9, 7, 9, 0, 0, 0, loc),
		End:   time.Date(2025, 9, 7, 11, 0, 0, 0, loc),
	}}

	first, err := buildICS("1234567890", outages, loc)
	if err != nil {
		t.Fatalf("buildICS() = %v", err)
	}
	second, err := buildICS("1234567890", outages, loc)
	if err != nil {
		t.Fatalf("buildICS() = %v", err)
	}

	// Subscribers refetch the feed constantly; identical input must render
	// identically or every poll looks like a change.
	if string(first) != string(second) {
		t.Errorf("buildICS() is not deterministic:\n%s\n---\n%s", first, second)
	}
}

func TestEventUID(t *testing.T) {
	loc := tehran(t)

	start := time.Date(2025, 9, 7, 9, 0, 0, 0, loc)
	end := time.Date(2025, 9, 7, 11, 0, 0, 0, loc)

	base := eventUID("1234567890", start, end)
	if !strings.HasSuffix(base, "@barghvim") {
		t.Errorf("eventUID() = %q, want an @barghvim suffix", base)
	}

	tests := []struct {
		name  string
		uid   string
		equal bool
	}{
		{"same inputs", eventUID("1234567890", start, end), true},
		{"same instant in another zone", eventUID("1234567890", start.UTC(), end.UTC()), true},
		{"different bill", eventUID("9999999999", start, end), false},
		{"different start", eventUID("1234567890", start.Add(time.Hour), end), false},
		{"different end", eventUID("1234567890", start, end.Add(time.Hour)), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if (tt.uid == base) != tt.equal {
				t.Errorf("eventUID() = %q, base = %q, want equal = %v", tt.uid, base, tt.equal)
			}
		})
	}
}

func TestBuildICSUsesCRLF(t *testing.T) {
	loc := tehran(t)

	outages := []Outage{{
		Start: time.Date(2025, 9, 7, 9, 0, 0, 0, loc),
		End:   time.Date(2025, 9, 7, 11, 0, 0, 0, loc),
	}}

	feed, err := buildICS("1234567890", outages, loc)
	if err != nil {
		t.Fatalf("buildICS() = %v", err)
	}
	got := string(feed)

	// RFC 5545 §3.1 mandates CRLF. Google Calendar accepts an LF-only feed
	// as a subscription and then shows no events at all.
	if !strings.HasSuffix(got, "END:VCALENDAR\r\n") {
		t.Errorf("feed does not end with a CRLF-terminated END:VCALENDAR:\n%q", got)
	}
	for i, line := range strings.Split(got, "\n") {
		if line == "" {
			continue // trailing element after the final CRLF
		}
		if !strings.HasSuffix(line, "\r") {
			t.Errorf("line %d is not CRLF-terminated: %q", i+1, line)
		}
	}
}
