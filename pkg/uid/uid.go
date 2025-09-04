package uid

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"
)

func EventUID(bill string, start time.Time, end time.Time) string {
	h := sha1.Sum(fmt.Appendf(nil, "%s|%d|%d", bill, start.Unix(), end.Unix()))
	return hex.EncodeToString(h[:]) + "@taghvim-bargh"
}
