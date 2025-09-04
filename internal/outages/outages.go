package outages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	httpc "github.com/MoKhajavi75/barghvim/pkg/http"
	"github.com/MoKhajavi75/barghvim/pkg/timeutil"
)

const apiURL = "https://uiapi2.saapa.ir/api/ebills/PlannedBlackoutsReport"

type Outage struct {
	Start time.Time // Asia/Tehran
	End   time.Time // Asia/Tehran
}

type reqBody struct {
	BillID string `json:"bill_id"`
	From   string `json:"from_date"` // Jalali yyyy/mm/dd
	To     string `json:"to_date"`   // Jalali yyyy/mm/dd
}

type respBody struct {
	Status  int        `json:"status"`
	Message string     `json:"message"`
	Data    []respItem `json:"data"`
}

type respItem struct {
	OutageDate string `json:"outage_date"`       // yyyy/mm/dd (Jalali)
	StartTime  string `json:"outage_start_time"` // HH:MM
	StopTime   string `json:"outage_stop_time"`  // HH:MM
}

func Fetch(ctx context.Context, token string, bill string) ([]Outage, error) {

	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		return nil, err
	}

	from := time.Now()
	to := from.Add(7 * 24 * time.Hour)

	body := reqBody{
		BillID: bill,
		From:   timeutil.ToJalaliYMD(from.In(loc), loc),
		To:     timeutil.ToJalaliYMD(to.In(loc), loc),
	}
	reqBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpc.Default.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream http %d", resp.StatusCode)
	}

	var rb respBody
	if err := json.NewDecoder(resp.Body).Decode(&rb); err != nil {
		return nil, err
	}

	if rb.Status != 200 {
		return nil, fmt.Errorf("upstream status %d: %s", rb.Status, rb.Message)
	}

	out := make([]Outage, 0, len(rb.Data))
	for _, item := range rb.Data {
		start, err := timeutil.FromJalaliYMDHM(item.OutageDate, item.StartTime, loc)
		if err != nil {
			return nil, fmt.Errorf("bad start time (%s %s): %w", item.OutageDate, item.StartTime, err)
		}

		end, err := timeutil.FromJalaliYMDHM(item.OutageDate, item.StopTime, loc)
		if err != nil {
			return nil, fmt.Errorf("bad stop time (%s %s): %w", item.OutageDate, item.StopTime, err)
		}

		if !end.After(start) {
			return nil, fmt.Errorf("stop before start (%s %s-%s)", item.OutageDate, item.StartTime, item.StopTime)
		}

		out = append(out, Outage{
			Start: start,
			End:   end,
		})
	}

	return out, nil
}
