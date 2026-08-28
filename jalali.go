package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	ptime "github.com/yaa110/go-persian-calendar"
)

// parseJalaliStamp converts a Jalali "yyyy/mm/dd HH:MM" stamp into the
// equivalent Gregorian instant in loc.
func parseJalaliStamp(stamp string, loc *time.Location) (time.Time, error) {
	date, clock, ok := strings.Cut(strings.TrimSpace(stamp), " ")
	if !ok {
		return time.Time{}, fmt.Errorf("jalali stamp %q: want %q", stamp, "yyyy/mm/dd HH:MM")
	}
	return parseJalaliDateTime(date, clock, loc)
}

// parseJalaliDateTime converts a Jalali date ("yyyy/mm/dd") plus a clock time
// ("HH:MM") into the equivalent Gregorian instant in loc.
func parseJalaliDateTime(date, clock string, loc *time.Location) (time.Time, error) {
	ymd, err := intFields(date, "/", 3)
	if err != nil {
		return time.Time{}, fmt.Errorf("jalali date %q: %w", date, err)
	}
	hm, err := intFields(clock, ":", 2)
	if err != nil {
		return time.Time{}, fmt.Errorf("clock time %q: %w", clock, err)
	}

	year, month, day := ymd[0], ymd[1], ymd[2]
	hour, minute := hm[0], hm[1]

	if year < 1 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, fmt.Errorf("jalali date %q: out of range", date)
	}
	// The report uses "24:00" for midnight at the end of a day; ptime.Date
	// normalizes that into 00:00 of the following day.
	if hour < 0 || hour > 24 || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("clock time %q: out of range", clock)
	}

	return ptime.Date(year, ptime.Month(month), day, hour, minute, 0, 0, loc).Time().In(loc), nil
}

// intFields splits s on sep and parses exactly n integer fields.
func intFields(s, sep string, n int) ([]int, error) {
	parts := strings.Split(strings.TrimSpace(s), sep)
	if len(parts) != n {
		return nil, fmt.Errorf("want %d fields separated by %q, got %d", n, sep, len(parts))
	}

	out := make([]int, n)
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("field %d: %w", i+1, err)
		}
		out[i] = v
	}
	return out, nil
}
