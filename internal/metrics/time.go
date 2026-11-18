package metrics

import (
	"fmt"
	"time"
)

// NormalizeRange returns a half-open millisecond range and its local timezone.
// The default is the current local day plus the preceding 29 days.
func NormalizeRange(from, to int64, timezone string, now time.Time) (int64, int64, *time.Location, error) {
	location := time.Local
	var err error
	if timezone != "" {
		location, err = time.LoadLocation(timezone)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("invalid timezone: %w", err)
		}
	}
	localNow := now.In(location)
	if to == 0 {
		to = now.UnixMilli() + 1
	}
	if from == 0 {
		start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).AddDate(0, 0, -29)
		from = start.UnixMilli()
	}
	if from < 0 || to <= from {
		return 0, 0, nil, fmt.Errorf("invalid time range")
	}
	return from, to, location, nil
}

func DayKey(milliseconds int64, location *time.Location) string {
	return time.UnixMilli(milliseconds).In(location).Format("2006-01-02")
}

func DayKeys(from, to int64, location *time.Location) []string {
	localFrom := time.UnixMilli(from).In(location)
	day := time.Date(localFrom.Year(), localFrom.Month(), localFrom.Day(), 0, 0, 0, 0, location)
	localTo := time.UnixMilli(to - 1).In(location)
	last := time.Date(localTo.Year(), localTo.Month(), localTo.Day(), 0, 0, 0, 0, location)
	keys := make([]string, 0, 31)
	for !day.After(last) {
		keys = append(keys, day.Format("2006-01-02"))
		day = day.AddDate(0, 0, 1)
	}
	return keys
}
