package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	ics "github.com/arran4/golang-ical"
)

const (
	productID    = "-//MoKhajavi75//barghvim//EN"
	repoURL      = "https://github.com/mokhajavi75/barghvim"
	eventSummary = "⚡️ Planned Power Outage"

	// refreshInterval tells subscribing clients how often to re-poll the feed.
	refreshInterval = "PT1H"
)

// buildICS renders outages as an iCalendar feed. Its output depends only on
// its inputs, so a feed refetched with unchanged data is byte-identical.
func buildICS(bill string, outages []Outage, loc *time.Location) ([]byte, error) {
	name := "Power Outages – " + bill

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	cal.SetProductId(productID)
	cal.SetVersion("2.0")
	cal.SetCalscale("GREGORIAN")
	cal.SetUrl(repoURL)
	cal.SetName(name)
	cal.SetXWRCalName(name)
	cal.SetXWRTimezone(loc.String())
	cal.SetRefreshInterval(refreshInterval)
	cal.SetXPublishedTTL(refreshInterval)

	for _, o := range outages {
		// DTSTART/DTEND are emitted as UTC timestamps; X-WR-TIMEZONE above
		// tells clients which zone to render them in.
		ev := cal.AddEvent(eventUID(bill, o.Start, o.End))
		// DTSTAMP is required by RFC 5545. Deriving it from the event rather
		// than from time.Now keeps repeated renders identical.
		ev.SetDtStampTime(o.Start.UTC())
		ev.SetSummary(eventSummary)
		ev.SetStatus(ics.ObjectStatusConfirmed)
		ev.SetClass(ics.ClassificationPrivate)
		ev.SetTimeTransparency(ics.TransparencyTransparent)
		ev.SetStartAt(o.Start)
		ev.SetEndAt(o.End)
	}

	// RFC 5545 §3.1 requires CRLF between content lines, but golang-ical
	// defaults to the host OS newline — bare LF on Linux and macOS. Apple
	// Calendar accepts that; Google Calendar subscribes to the feed and then
	// renders nothing. Ask for CRLF explicitly rather than inherit the OS.
	var buf bytes.Buffer
	if err := cal.SerializeTo(&buf, ics.WithNewLineWindows); err != nil {
		return nil, fmt.Errorf("serializing calendar: %w", err)
	}
	return buf.Bytes(), nil
}

// eventUID derives a stable per-event identifier so refetching the feed
// updates existing calendar entries instead of duplicating them.
func eventUID(bill string, start, end time.Time) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%d", bill, start.Unix(), end.Unix()))
	return hex.EncodeToString(sum[:16]) + "@barghvim"
}
