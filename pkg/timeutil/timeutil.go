package timeutil

import (
	"fmt"
	"time"

	ptime "github.com/yaa110/go-persian-calendar"
)

// ToJalaliYMD converts a time to "yyyy/mm/dd" (Jalali) in loc.
func ToJalaliYMD(t time.Time, loc *time.Location) string {
	pt := ptime.New(t.In(loc))

	return fmt.Sprintf("%04d/%02d/%02d", pt.Year(), pt.Month(), pt.Day())
}

// FromJalaliYMDHM builds a time from "yyyy/mm/dd" (Jalali) + "HH:MM" in loc.
func FromJalaliYMDHM(ymd string, hm string, loc *time.Location) (time.Time, error) {
	var y, m, d, hh, mm int
	if _, err := fmt.Sscanf(ymd, "%d/%d/%d", &y, &m, &d); err != nil {
		return time.Time{}, err
	}
	if _, err := fmt.Sscanf(hm, "%d:%d", &hh, &mm); err != nil {
		return time.Time{}, err
	}
	jt := ptime.Date(y, ptime.Month(m), d, hh, mm, 0, 0, loc).Time()

	return jt.In(loc), nil
}
