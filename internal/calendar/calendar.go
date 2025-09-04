package calendar

import (
	"time"

	"github.com/MoKhajavi75/barghvim/internal/outages"
	"github.com/MoKhajavi75/barghvim/pkg/uid"
	ics "github.com/arran4/golang-ical"
)

const tzid = "Asia/Tehran"

func BuildICS(bill string, items []outages.Outage) ([]byte, error) {
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil, err
	}

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	cal.SetProductId("-//MoKhajavi75//barghvim 1.0.0//EN")
	cal.SetUrl("https://github.com/mokhajavi75/barghvim")
	cal.SetName("Power Outages – " + bill)
	cal.SetCalscale("GREGORIAN")

	for _, o := range items {
		start := o.Start.In(loc)
		end := o.End.In(loc)

		ev := cal.AddEvent(uid.EventUID(bill, start, end))
		ev.SetSummary("Planned Power Outage")
		ev.SetTimeTransparency(ics.TransparencyTransparent)
		ev.SetStartAt(start)
		ev.SetEndAt(end)
	}

	return []byte(cal.Serialize()), nil
}
