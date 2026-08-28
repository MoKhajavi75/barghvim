package main

import (
	"fmt"
	"testing"
	"time"

	ptime "github.com/yaa110/go-persian-calendar"
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

func TestParseJalaliStamp(t *testing.T) {
	loc := tehran(t)

	tests := []struct {
		name    string
		stamp   string
		want    time.Time
		wantErr bool
	}{
		{"nowruz", "1404/01/01 09:30", time.Date(2025, 3, 21, 9, 30, 0, 0, loc), false},
		{"mid year", "1404/06/16 09:00", time.Date(2025, 9, 7, 9, 0, 0, 0, loc), false},
		{"leap day", "1403/12/30 00:00", time.Date(2025, 3, 20, 0, 0, 0, 0, loc), false},
		{"surrounding whitespace", " 1404/06/16 09:00 ", time.Date(2025, 9, 7, 9, 0, 0, 0, loc), false},
		{"no time part", "1404/06/16", time.Time{}, true},
		{"empty", "", time.Time{}, true},
		{"separated by T", "1404/06/16T09:00", time.Time{}, true},
		{"garbage", "not a stamp", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJalaliStamp(tt.stamp, loc)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseJalaliStamp(%q) = %s, want error", tt.stamp, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJalaliStamp(%q) = %v", tt.stamp, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseJalaliStamp(%q) = %s, want %s",
					tt.stamp, got.Format(time.RFC3339), tt.want.Format(time.RFC3339))
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
		p := ptime.New(day)
		stamp := fmt.Sprintf("%04d/%02d/%02d 12:00", p.Year(), p.Month(), p.Day())

		got, err := parseJalaliStamp(stamp, loc)
		if err != nil {
			t.Fatalf("parseJalaliStamp(%q) = %v", stamp, err)
		}
		if !got.Equal(day) {
			t.Fatalf("round trip of %s via %q = %s", day.Format(time.RFC3339), stamp, got.Format(time.RFC3339))
		}

		day = day.AddDate(0, 0, 1)
	}
}
