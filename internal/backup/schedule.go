package backup

import (
	"errors"
	"time"
	_ "time/tzdata"
)

// NextOccurrence returns the first configured time strictly after now. Wall-clock
// times missing during a DST spring-forward are skipped; repeated fall-back
// times run once, at their earlier instant.
func NextOccurrence(schedule Schedule, now time.Time) (time.Time, bool, error) {
	if err := validateSchedule(schedule); err != nil {
		return time.Time{}, false, err
	}
	if !schedule.WeeklyEnabled && !schedule.MonthlyEnabled {
		return time.Time{}, false, nil
	}
	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return time.Time{}, false, errors.New("backup timezone is invalid")
	}
	now = now.In(loc)
	var next time.Time
	if schedule.WeeklyEnabled {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		for offset := 0; offset <= 14; offset++ {
			candidateDay := day.AddDate(0, 0, offset)
			if candidateDay.Weekday() != schedule.WeeklyDay {
				continue
			}
			candidate, exists := wallClockTime(candidateDay, schedule.WeeklyHour, schedule.WeeklyMinute)
			if exists && candidate.After(now) && (next.IsZero() || candidate.Before(next)) {
				next = candidate
			}
		}
	}
	if schedule.MonthlyEnabled {
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		for offset := 0; offset <= 1; offset++ {
			candidateMonth := month.AddDate(0, offset, 0)
			candidateDay := time.Date(candidateMonth.Year(), candidateMonth.Month(), schedule.MonthlyDay, 0, 0, 0, 0, loc)
			candidate, exists := wallClockTime(candidateDay, schedule.MonthlyHour, schedule.MonthlyMinute)
			if exists && candidate.After(now) && (next.IsZero() || candidate.Before(next)) {
				next = candidate
			}
		}
	}
	return next, !next.IsZero(), nil
}

func wallClockTime(day time.Time, hour, minute int) (time.Time, bool) {
	loc := day.Location()
	approx := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
	// time.Date normalizes a nonexistent wall time and chooses an unspecified side
	// of an ambiguous wall time. Search nearby real instants to detect gaps and
	// deliberately select the earlier match in a fold.
	start, end := approx.Add(-4*time.Hour), approx.Add(4*time.Hour)
	for candidate := start; !candidate.After(end); candidate = candidate.Add(time.Minute) {
		local := candidate.In(loc)
		if local.Year() == day.Year() && local.Month() == day.Month() && local.Day() == day.Day() &&
			local.Hour() == hour && local.Minute() == minute && local.Second() == 0 && local.Nanosecond() == 0 {
			return candidate, true
		}
	}
	return time.Time{}, false
}
