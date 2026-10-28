package warmup

import (
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// LatestMissedSlot returns the most recent schedule occurrence at or before now
// that has not been completed yet, or a zero time when there is nothing to run.
//
// A slot is a distinct daily occurrence of one configured HH:MM in the
// configured zone, identified by that local wall-clock date and time. Slots
// older than the newest one are never returned: the warmup is an idempotent
// "start the window" action, so a long outage only needs the latest slot
// instead of replaying every day it was down.
//
// lastCompleted is the most recently completed slot (zero when none). When it
// is at or after a candidate, that candidate has already run and is skipped.
func LatestMissedSlot(now time.Time, warmup config.WarmupConfig, lastCompleted time.Time) (time.Time, error) {
	location, errLocation := warmup.Location()
	if errLocation != nil {
		return time.Time{}, errLocation
	}
	offsets, errOffsets := warmup.ScheduleTimes()
	if errOffsets != nil {
		return time.Time{}, errOffsets
	}
	local := now.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	// Look back over two days so a late-evening slot still counts when the
	// process starts shortly after midnight.
	var latest time.Time
	for back := 0; back <= 1; back++ {
		candidateDay := day.AddDate(0, 0, -back)
		for _, offset := range offsets {
			candidate := wallClock(candidateDay.Year(), candidateDay.Month(), candidateDay.Day(), offset, location)
			if candidate.After(now) {
				continue
			}
			if candidate.After(latest) {
				latest = candidate
			}
		}
	}
	if latest.IsZero() {
		return time.Time{}, nil
	}
	if !lastCompleted.IsZero() && !latest.After(lastCompleted) {
		return time.Time{}, nil
	}
	return latest, nil
}

// NextSlot returns the first schedule occurrence strictly after now.
func NextSlot(now time.Time, warmup config.WarmupConfig) (time.Time, error) {
	location, errLocation := warmup.Location()
	if errLocation != nil {
		return time.Time{}, errLocation
	}
	offsets, errOffsets := warmup.ScheduleTimes()
	if errOffsets != nil {
		return time.Time{}, errOffsets
	}
	if len(offsets) == 0 {
		return time.Time{}, fmt.Errorf("warmup schedule is empty")
	}
	local := now.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	for back := 0; back <= 2; back++ {
		candidateDay := day.AddDate(0, 0, back)
		for _, offset := range offsets {
			candidate := wallClock(candidateDay.Year(), candidateDay.Month(), candidateDay.Day(), offset, location)
			if candidate.After(now) {
				return candidate, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("warmup schedule has no future occurrence")
}

// wallClock resolves a local wall-clock time in a zone. time.Date already picks
// the earliest instant for an ambiguous (repeated) hour, which keeps a slot from
// firing twice on a fall-back day; for a skipped hour it lands on the instant
// that immediately follows the gap.
func wallClock(year int, month time.Month, day int, offset time.Duration, location *time.Location) time.Time {
	hour := int(offset / time.Hour)
	minute := int((offset % time.Hour) / time.Minute)
	return time.Date(year, month, day, hour, minute, 0, 0, location)
}
