// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Please see https://officer.openpit.dev and the OWNERS file for details.

package scheduler

import (
	"fmt"
	"math"
	"time"
)

// First returns the first occurrence, including one already overdue. A pending
// one-off survives downtime. Recurrences run at most one overdue occurrence;
// Next then advances directly to a future boundary on the original anchor.
func First(spec JobSpec) (Occurrence, bool, error) {
	if err := spec.Validate(); err != nil {
		return Occurrence{}, false, err
	}
	first := Occurrence{At: spec.Schedule.StartAt.UTC()}
	if spec.Schedule.Kind == Daily {
		var err error
		first, err = nextDaily(spec.Schedule, spec.Schedule.StartAt, "", true)
		if err != nil {
			return Occurrence{}, false, err
		}
	}
	return first, withinEnd(spec, first, 0), nil
}

// Next calculates a boundary strictly after both previous and now, retaining
// the interval/calendar anchor. completed is the total number of successful
// executions, including the one just finished. A false result means that the
// one-off or explicit recurrence termination has completed.
func Next(
	spec JobSpec, previous Occurrence, now time.Time, completed int64,
) (Occurrence, bool, error) {
	if err := spec.Validate(); err != nil {
		return Occurrence{}, false, err
	}
	if completed < 0 {
		return Occurrence{}, false, fmt.Errorf("negative completed count: %w", ErrInvalid)
	}
	if spec.Schedule.Kind == Once ||
		(spec.Termination.Mode == Count && completed >= spec.Termination.MaxRuns) {
		return Occurrence{}, false, nil
	}
	after := now
	if previous.At.After(after) {
		after = previous.At
	}
	if spec.Termination.Mode == Until && !after.Before(spec.Termination.Until) {
		return Occurrence{}, false, nil
	}
	var next Occurrence
	if spec.Schedule.Kind == Interval {
		anchor, every := spec.Schedule.StartAt, spec.Schedule.Every
		if after.Before(anchor) {
			next.At = anchor.UTC()
		} else {
			elapsed := after.Sub(anchor)
			// time.Sub saturates outside duration's range. Do not turn an
			// unrepresentable elapsed interval into a wrong future boundary.
			if anchor.Add(elapsed).Before(after) ||
				elapsed/every >= time.Duration(math.MaxInt64)/every {
				return Occurrence{}, false, fmt.Errorf("interval boundary exceeds duration range")
			}
			steps := elapsed/every + 1
			next.At = anchor.Add(steps * every).UTC()
		}
	} else {
		var err error
		next, err = nextDaily(spec.Schedule, after, previous.Day, false)
		if err != nil {
			return Occurrence{}, false, err
		}
	}
	return next, withinEnd(spec, next, completed), nil
}

func withinEnd(spec JobSpec, occurrence Occurrence, completed int64) bool {
	switch spec.Termination.Mode {
	case Count:
		return completed < spec.Termination.MaxRuns
	case Until:
		return !occurrence.At.After(spec.Termination.Until)
	default:
		return true
	}
}

func nextDaily(
	s Schedule, after time.Time, previousDay string, inclusive bool,
) (Occurrence, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return Occurrence{}, err
	}
	local := after.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if previousDay != "" && date.Format("2006-01-02") < previousDay {
		date, err = time.Parse("2006-01-02", previousDay)
		if err != nil {
			return Occurrence{}, fmt.Errorf("invalid previous calendar day: %w", err)
		}
	}
	for offset := 0; offset < 8; offset++ {
		dayDate := date.AddDate(0, 0, offset)
		day := dayDate.Format("2006-01-02")
		if day <= previousDay {
			continue
		}
		occurrence, exists := dailyOnDate(s.Time, day, dayDate, loc)
		if !exists {
			// An entire date can disappear in an IANA timezone transition.
			// It contains no scheduled minute; continue to the next date.
			continue
		}
		if occurrence.At.After(after) || (inclusive && occurrence.At.Equal(after)) {
			return occurrence, nil
		}
	}
	return Occurrence{}, fmt.Errorf("no daily boundary within eight calendar days")
}

func dailyOnDate(clock, day string, date time.Time, loc *time.Location) (Occurrence, bool) {
	// Search instants rather than time.Date's ambiguous DST normalization.
	// A missing minute maps to the first later minute and is explicitly skipped.
	for instant, end := date.Add(-26*time.Hour), date.Add(50*time.Hour); instant.Before(end); instant = instant.Add(time.Minute) {
		local := instant.In(loc)
		if local.Format("2006-01-02") != day {
			continue
		}
		minute := local.Format("15:04")
		if minute >= clock {
			return Occurrence{At: instant, Day: day, Skip: minute != clock}, true
		}
	}
	return Occurrence{}, false
}
