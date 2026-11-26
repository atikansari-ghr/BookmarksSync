package backup

import (
	"testing"
	"time"
)

func TestNextOccurrenceUsesConfiguredWeeklyTime(t *testing.T) {
	schedule := DefaultSchedule()
	schedule.WeeklyEnabled = true
	now := time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)
	got, ok, err := NextOccurrence(schedule, now)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence = %v, %v, %v", got, ok, err)
	}
	loc, _ := time.LoadLocation("Asia/Riyadh")
	want := time.Date(2026, 11, 22, 3, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("weekly occurrence = %v, want %v", got, want)
	}
}

func TestNextOccurrenceIsStrictlyFutureAndSkipsMissedSlots(t *testing.T) {
	schedule := DefaultSchedule()
	schedule.WeeklyEnabled = true
	loc, _ := time.LoadLocation(schedule.Timezone)
	occurrence := time.Date(2026, 11, 22, 3, 0, 0, 0, loc)
	got, ok, err := NextOccurrence(schedule, occurrence)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence at boundary = %v, %v, %v", got, ok, err)
	}
	want := occurrence.AddDate(0, 0, 7)
	if !got.Equal(want) {
		t.Fatalf("boundary occurrence = %v, want next week %v", got, want)
	}
	got, ok, err = NextOccurrence(schedule, occurrence.Add(4*time.Hour))
	if err != nil || !ok || !got.Equal(want) {
		t.Fatalf("missed occurrence should be skipped: got %v, %v, %v; want %v", got, ok, err, want)
	}
}

func TestNextOccurrenceChoosesEarlierEnabledSchedule(t *testing.T) {
	schedule := DefaultSchedule()
	schedule.WeeklyEnabled = true
	schedule.MonthlyEnabled = true
	loc, _ := time.LoadLocation(schedule.Timezone)
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, loc)
	got, ok, err := NextOccurrence(schedule, now)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence = %v, %v, %v", got, ok, err)
	}
	want := time.Date(2026, 12, 1, 4, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("next occurrence = %v, want monthly time %v", got, want)
	}
}

func TestNextOccurrenceSkipsNonexistentDSTWallTime(t *testing.T) {
	schedule := DefaultSchedule()
	schedule.Timezone = "America/New_York"
	schedule.WeeklyEnabled = true
	schedule.WeeklyHour = 2
	schedule.WeeklyMinute = 30
	loc, _ := time.LoadLocation(schedule.Timezone)
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, loc)
	got, ok, err := NextOccurrence(schedule, now)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence = %v, %v, %v", got, ok, err)
	}
	want := time.Date(2026, 3, 15, 2, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("nonexistent DST time should skip to next week: got %v, want %v", got, want)
	}
}

func TestNextOccurrenceRunsFoldedDSTWallTimeAtMostOnce(t *testing.T) {
	schedule := DefaultSchedule()
	schedule.Timezone = "America/New_York"
	schedule.WeeklyEnabled = true
	schedule.WeeklyHour = 1
	schedule.WeeklyMinute = 30
	loc, _ := time.LoadLocation(schedule.Timezone)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, loc)
	first, ok, err := NextOccurrence(schedule, now)
	if err != nil || !ok || first.In(loc).Day() != 1 || first.In(loc).Hour() != 1 || first.In(loc).Minute() != 30 {
		t.Fatalf("folded occurrence = %v, %v, %v", first, ok, err)
	}
	second, ok, err := NextOccurrence(schedule, first.Add(time.Minute))
	want := time.Date(2026, 11, 8, 1, 30, 0, 0, loc)
	if err != nil || !ok || !second.Equal(want) {
		t.Fatalf("scheduler should not run the repeated wall time twice: got %v, %v, %v; want %v", second, ok, err, want)
	}
}

func TestNextOccurrenceReturnsFalseWhenDisabled(t *testing.T) {
	got, ok, err := NextOccurrence(DefaultSchedule(), time.Now())
	if err != nil || ok || !got.IsZero() {
		t.Fatalf("disabled schedule = %v, %v, %v", got, ok, err)
	}
}
