package main

import (
	"testing"
	"time"
)

// tehran returns the Asia/Tehran location, which the embedded tzdata makes
// available on every platform.
func tehran(t *testing.T) *time.Location {
	t.Helper()

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		t.Fatalf("LoadLocation(%q) = %v", timezone, err)
	}
	return loc
}

func TestJalaliDate(t *testing.T) {
	loc := tehran(t)

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"nowruz 1404", time.Date(2025, 3, 21, 12, 0, 0, 0, loc), "1404/01/01"},
		{"mid year", time.Date(2025, 9, 7, 12, 0, 0, 0, loc), "1404/06/16"},
		{"converts from utc", time.Date(2025, 9, 7, 8, 30, 0, 0, time.UTC), "1404/06/16"},
		{"last day before nowruz", time.Date(2025, 3, 20, 12, 0, 0, 0, loc), "1403/12/30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jalaliDate(tt.in, loc); got != tt.want {
				t.Errorf("jalaliDate(%s) = %q, want %q", tt.in.Format(time.RFC3339), got, tt.want)
			}
		})
	}
}

func TestParseJalaliDateTime(t *testing.T) {
	loc := tehran(t)

	tests := []struct {
		name    string
		date    string
		clock   string
		want    time.Time
		wantErr bool
	}{
		{
			name:  "nowruz",
			date:  "1404/01/01",
			clock: "09:30",
			want:  time.Date(2025, 3, 21, 9, 30, 0, 0, loc),
		},
		{
			name:  "mid year",
			date:  "1404/06/16",
			clock: "09:00",
			want:  time.Date(2025, 9, 7, 9, 0, 0, 0, loc),
		},
		{
			name:  "leap day",
			date:  "1403/12/30",
			clock: "00:00",
			want:  time.Date(2025, 3, 20, 0, 0, 0, 0, loc),
		},
		{
			name:  "hour 24 rolls into the next day",
			date:  "1404/06/16",
			clock: "24:00",
			want:  time.Date(2025, 9, 8, 0, 0, 0, 0, loc),
		},
		{
			name:  "surrounding whitespace is tolerated",
			date:  " 1404/06/16 ",
			clock: " 09:00 ",
			want:  time.Date(2025, 9, 7, 9, 0, 0, 0, loc),
		},
		{"empty date", "", "09:00", time.Time{}, true},
		{"empty clock", "1404/06/16", "", time.Time{}, true},
		{"too few date fields", "1404/06", "09:00", time.Time{}, true},
		{"too many date fields", "1404/06/16/01", "09:00", time.Time{}, true},
		{"non numeric date", "1404/aa/16", "09:00", time.Time{}, true},
		{"non numeric clock", "1404/06/16", "aa:00", time.Time{}, true},
		{"month out of range", "1404/13/01", "09:00", time.Time{}, true},
		{"month zero", "1404/00/01", "09:00", time.Time{}, true},
		{"day out of range", "1404/06/32", "09:00", time.Time{}, true},
		{"hour out of range", "1404/06/16", "25:00", time.Time{}, true},
		{"minute out of range", "1404/06/16", "09:60", time.Time{}, true},
		{"negative year", "-1/06/16", "09:00", time.Time{}, true},
		{"dash separated date", "1404-06-16", "09:00", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJalaliDateTime(tt.date, tt.clock, loc)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseJalaliDateTime(%q, %q) = %s, want error", tt.date, tt.clock, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJalaliDateTime(%q, %q) = %v", tt.date, tt.clock, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseJalaliDateTime(%q, %q) = %s, want %s",
					tt.date, tt.clock, got.Format(time.RFC3339), tt.want.Format(time.RFC3339))
			}
		})
	}
}

func TestJalaliRoundTrip(t *testing.T) {
	loc := tehran(t)

	// A whole Jalali year of dates must survive a conversion in both
	// directions unchanged.
	day := time.Date(2025, 3, 21, 12, 0, 0, 0, loc)
	for range 365 {
		date := jalaliDate(day, loc)

		got, err := parseJalaliDateTime(date, "12:00", loc)
		if err != nil {
			t.Fatalf("parseJalaliDateTime(%q) = %v", date, err)
		}
		if !got.Equal(day) {
			t.Fatalf("round trip of %s via %q = %s", day.Format(time.RFC3339), date, got.Format(time.RFC3339))
		}

		day = day.AddDate(0, 0, 1)
	}
}
