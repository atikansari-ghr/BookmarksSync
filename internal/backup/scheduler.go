package backup

import (
	"context"
	"errors"
	"time"
)

type RunFunc func(context.Context, Trigger) error
type LoadScheduleFunc func(context.Context) (Schedule, error)
type ScheduleWaitFunc func(context.Context, time.Duration, <-chan struct{}) (changed bool, err error)

type Scheduler struct {
	Load      LoadScheduleFunc
	RunBackup RunFunc
	Changed   <-chan struct{}
	Now       func() time.Time
	Wait      ScheduleWaitFunc
}

// Run waits for future schedule slots. It never catches up for a slot missed
// while the process was offline; after each run it calculates from the current
// time, and an admin change interrupts the pending timer.
func (s Scheduler) Run(ctx context.Context) error {
	if s.Load == nil || s.RunBackup == nil {
		return errors.New("backup scheduler requires schedule loader and runner")
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	wait := s.Wait
	if wait == nil {
		wait = waitForSchedule
	}
	var lastAttempted time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		schedule, err := s.Load(ctx)
		if err != nil {
			changed, waitErr := wait(ctx, time.Minute, s.Changed)
			if waitErr != nil {
				return waitErr
			}
			if changed {
				continue
			}
			continue
		}
		current := now()
		next, enabled, err := NextOccurrence(schedule, current)
		if err != nil {
			changed, waitErr := wait(ctx, time.Minute, s.Changed)
			if waitErr != nil {
				return waitErr
			}
			if changed {
				continue
			}
			continue
		}
		if enabled && !lastAttempted.IsZero() && !next.After(lastAttempted) {
			next, enabled, err = NextOccurrence(schedule, lastAttempted.Add(time.Second))
			if err != nil {
				continue
			}
		}
		delay := time.Hour
		if enabled {
			delay = next.Sub(current)
			if delay < 0 {
				delay = 0
			}
			if delay > time.Hour {
				delay = time.Hour
			}
		}
		changed, waitErr := wait(ctx, delay, s.Changed)
		if waitErr != nil {
			return waitErr
		}
		if changed || !enabled {
			continue
		}
		current = now()
		if current.Before(next) || current.Sub(next) > time.Minute {
			continue
		}
		lastAttempted = next
		for _, trigger := range triggersAt(schedule, next) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.RunBackup(ctx, trigger); err != nil && errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

func triggersAt(schedule Schedule, at time.Time) []Trigger {
	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return nil
	}
	local := at.In(loc)
	var triggers []Trigger
	if schedule.WeeklyEnabled && local.Weekday() == schedule.WeeklyDay {
		if candidate, ok := wallClockTime(local, schedule.WeeklyHour, schedule.WeeklyMinute); ok && candidate.Equal(at) {
			triggers = append(triggers, TriggerWeekly)
		}
	}
	if schedule.MonthlyEnabled && local.Day() == schedule.MonthlyDay {
		if candidate, ok := wallClockTime(local, schedule.MonthlyHour, schedule.MonthlyMinute); ok && candidate.Equal(at) {
			triggers = append(triggers, TriggerMonthly)
		}
	}
	return triggers
}

func waitForSchedule(ctx context.Context, delay time.Duration, changed <-chan struct{}) (bool, error) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-changed:
		return true, nil
	case <-timer.C:
		return false, nil
	}
}
