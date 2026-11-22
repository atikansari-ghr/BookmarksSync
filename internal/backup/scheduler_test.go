package backup

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type schedulerWaitResult struct {
	changed bool
	err     error
}

type schedulerHarness struct {
	loads     int
	schedules []Schedule
	loadErrs  []error
	waits     []schedulerWaitResult
	delays    []time.Duration
	runs      []Trigger
	clock     time.Time
	afterWait func(int)
}

func (h *schedulerHarness) load(context.Context) (Schedule, error) {
	h.loads++
	index := h.loads - 1
	if index < len(h.loadErrs) && h.loadErrs[index] != nil {
		return Schedule{}, h.loadErrs[index]
	}
	if index >= len(h.schedules) {
		index = len(h.schedules) - 1
	}
	return h.schedules[index], nil
}

func (h *schedulerHarness) wait(_ context.Context, delay time.Duration, _ <-chan struct{}) (bool, error) {
	h.delays = append(h.delays, delay)
	index := len(h.delays) - 1
	if index >= len(h.waits) {
		return false, errors.New("unexpected wait")
	}
	result := h.waits[index]
	if !result.changed && result.err == nil {
		h.clock = h.clock.Add(delay)
	}
	if h.afterWait != nil {
		h.afterWait(index)
	}
	return result.changed, result.err
}

func TestSchedulerRecomputesAndSkipsWallClockJumpPastSlot(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	due := time.Date(2026, 11, 22, 3, 0, 0, 0, loc)
	s := DefaultSchedule()
	s.WeeklyEnabled = true
	h := &schedulerHarness{clock: due.Add(-time.Hour), schedules: []Schedule{s}, loadErrs: []error{nil, context.Canceled}, waits: []schedulerWaitResult{{}, {err: context.Canceled}}}
	h.afterWait = func(i int) {
		if i == 0 {
			h.clock = due.Add(2 * time.Minute)
		}
	}
	scheduler := Scheduler{Load: h.load, RunBackup: h.run, Now: func() time.Time { return h.clock }, Wait: h.wait}
	if err := scheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v", err)
	}
	if len(h.runs) != 0 || h.delays[0] != time.Hour {
		t.Fatalf("runs=%v delays=%v; clock jump should skip missed slot", h.runs, h.delays)
	}
}

func TestSchedulerDoesNotRepeatOccurrenceAfterClockMovesBackward(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	due := time.Date(2026, 11, 22, 3, 0, 0, 0, loc)
	s := DefaultSchedule()
	s.WeeklyEnabled = true
	h := &schedulerHarness{clock: due.Add(-time.Hour), schedules: []Schedule{s, s, s}, waits: []schedulerWaitResult{{}, {}, {err: context.Canceled}}}
	h.afterWait = func(i int) {
		if i == 1 {
			h.clock = due.Add(-time.Hour)
		}
	}
	scheduler := Scheduler{Load: h.load, RunBackup: h.run, Now: func() time.Time { return h.clock }, Wait: h.wait}
	if err := scheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v", err)
	}
	if !reflect.DeepEqual(h.runs, []Trigger{TriggerWeekly}) {
		t.Fatalf("clock rollback repeated schedule: %v", h.runs)
	}
}

func (h *schedulerHarness) run(_ context.Context, trigger Trigger) error {
	h.runs = append(h.runs, trigger)
	return nil
}

func TestSchedulerReloadsAfterConfigurationChange(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	h := &schedulerHarness{
		clock: time.Date(2026, 11, 22, 0, 0, 0, 0, loc),
		schedules: []Schedule{
			func() Schedule { s := DefaultSchedule(); s.WeeklyEnabled = true; return s }(),
			func() Schedule { s := DefaultSchedule(); s.WeeklyEnabled = true; s.WeeklyDay = time.Monday; return s }(),
		},
		waits: []schedulerWaitResult{{changed: true}, {changed: false, err: context.Canceled}},
	}
	scheduler := Scheduler{Load: h.load, RunBackup: h.run, Now: func() time.Time { return h.clock }, Wait: h.wait}
	if err := scheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	if h.loads != 2 || len(h.runs) != 0 {
		t.Fatalf("loaded %d schedules, ran %v; reconfiguration should replace pending time", h.loads, h.runs)
	}
	wantDelay := time.Hour
	if h.delays[1] != wantDelay {
		t.Fatalf("reconfigured delay = %v, want %v", h.delays[1], wantDelay)
	}
}

func TestSchedulerInvokesDueBackupAndReloads(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	due := time.Date(2026, 11, 22, 3, 0, 0, 0, loc)
	s := DefaultSchedule()
	s.WeeklyEnabled = true
	h := &schedulerHarness{clock: due.Add(-time.Hour), schedules: []Schedule{s}, loadErrs: []error{nil, context.Canceled}, waits: []schedulerWaitResult{{changed: false}, {changed: false, err: context.Canceled}}}
	scheduler := Scheduler{Load: h.load, RunBackup: h.run, Now: func() time.Time { return h.clock }, Wait: h.wait}
	if err := scheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if !reflect.DeepEqual(h.runs, []Trigger{TriggerWeekly}) {
		t.Fatalf("scheduled triggers = %v, want weekly", h.runs)
	}
	if h.delays[0] != time.Hour {
		t.Fatalf("wait delay = %v, want one hour", h.delays[0])
	}
}

func TestSchedulerRunsBothPoliciesWhenConfiguredForSameInstant(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	due := time.Date(2026, 11, 1, 3, 0, 0, 0, loc)
	s := DefaultSchedule()
	s.WeeklyEnabled, s.MonthlyEnabled = true, true
	s.WeeklyDay, s.WeeklyHour, s.WeeklyMinute = time.Sunday, 3, 0
	s.MonthlyDay, s.MonthlyHour, s.MonthlyMinute = 1, 3, 0
	h := &schedulerHarness{clock: due.Add(-time.Hour), schedules: []Schedule{s}, loadErrs: []error{nil, context.Canceled}, waits: []schedulerWaitResult{{changed: false}, {changed: false, err: context.Canceled}}}
	scheduler := Scheduler{Load: h.load, RunBackup: h.run, Now: func() time.Time { return h.clock }, Wait: h.wait}
	if err := scheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if !reflect.DeepEqual(h.runs, []Trigger{TriggerWeekly, TriggerMonthly}) {
		t.Fatalf("scheduled triggers = %v, want weekly then monthly", h.runs)
	}
}

func TestSchedulerRetriesSettingsReadFailureWithoutRunning(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	due := time.Date(2026, 11, 22, 3, 0, 0, 0, loc)
	loadCount := 0
	load := func(context.Context) (Schedule, error) {
		loadCount++
		if loadCount == 1 {
			return Schedule{}, errors.New("database unavailable")
		}
		s := DefaultSchedule()
		s.WeeklyEnabled = true
		return s, nil
	}
	waits := []schedulerWaitResult{{changed: false}, {changed: false, err: context.Canceled}}
	waitIndex := 0
	var delays []time.Duration
	wait := func(_ context.Context, delay time.Duration, _ <-chan struct{}) (bool, error) {
		delays = append(delays, delay)
		result := waits[waitIndex]
		waitIndex++
		return result.changed, result.err
	}
	var runs []Trigger
	scheduler := Scheduler{Load: load, RunBackup: func(_ context.Context, trigger Trigger) error { runs = append(runs, trigger); return nil }, Now: func() time.Time { return due.Add(-time.Hour) }, Wait: wait}
	if err := scheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if len(runs) != 0 || loadCount != 2 {
		t.Fatalf("run triggers=%v, schedule loads=%d; settings failure should retry without an unintended run", runs, loadCount)
	}
	if delays[0] != time.Minute {
		t.Fatalf("settings retry delay = %v, want one minute", delays[0])
	}
}
