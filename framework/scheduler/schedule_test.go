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

package scheduler_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/scheduler"
)

func dailySpec(clock, zone, start string) scheduler.JobSpec {
	instant, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	return scheduler.JobSpec{
		Kind: "example", Config: json.RawMessage(`null`),
		Schedule:    scheduler.Schedule{Kind: scheduler.Daily, StartAt: instant, Time: clock, Timezone: zone},
		Termination: scheduler.Termination{Mode: scheduler.Forever},
	}
}

func TestDailyDSTBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, clock, zone, start, want string
		skipped                        bool
	}{
		{"spring gap", "02:30", "America/New_York", "2030-03-10T06:00:00Z", "2030-03-10T07:00:00Z", true},
		{"overlap first", "01:30", "America/New_York", "2030-11-03T04:00:00Z", "2030-11-03T05:30:00Z", false},
		{"overlap second excluded", "01:30", "America/New_York", "2030-11-03T05:31:00Z", "2030-11-04T06:30:00Z", false},
		{"half hour gap", "02:15", "Australia/Lord_Howe", "2030-10-05T14:00:00Z", "2030-10-05T15:30:00Z", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, more, err := scheduler.First(dailySpec(test.clock, test.zone, test.start))
			if err != nil || !more || got.At.Format(time.RFC3339) != test.want || got.Skip != test.skipped {
				t.Fatalf("First=%+v %v %v, want %s skip=%v", got, more, err, test.want, test.skipped)
			}
		})
	}
}

func TestScheduleRejectsAmbiguousDefinitions(t *testing.T) {
	valid := dailySpec("09:00", "UTC", "2030-01-01T00:00:00Z")
	for _, change := range []func(*scheduler.JobSpec){
		func(s *scheduler.JobSpec) { s.Schedule.Time = "9:00" },
		func(s *scheduler.JobSpec) { s.Schedule.Time = "24:00" },
		func(s *scheduler.JobSpec) { s.Schedule.Timezone = "Local" },
		func(s *scheduler.JobSpec) { s.Schedule.Timezone = "Invalid/Zone" },
		func(s *scheduler.JobSpec) { s.Schedule.Timezone = "" },
		func(s *scheduler.JobSpec) { s.Schedule.Every = time.Hour },
		func(s *scheduler.JobSpec) { s.Termination.Mode = "" },
		func(s *scheduler.JobSpec) { s.Termination.MaxRuns = 100 },
		func(s *scheduler.JobSpec) { s.Termination = scheduler.Termination{Mode: scheduler.Count, MaxRuns: 0} },
		func(s *scheduler.JobSpec) { s.Termination = scheduler.Termination{Mode: scheduler.Until} },
		func(s *scheduler.JobSpec) { s.Schedule.Kind = scheduler.Once },
	} {
		spec := valid
		change(&spec)
		if err := spec.Validate(); !errors.Is(err, scheduler.ErrInvalid) {
			t.Fatalf("invalid spec accepted: %+v %v", spec, err)
		}
	}
}

func TestIntervalAndTerminationBoundaries(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	spec := scheduler.JobSpec{Kind: "example", Config: json.RawMessage(`123`),
		Schedule:    scheduler.Schedule{Kind: scheduler.Interval, StartAt: start, Every: time.Hour},
		Termination: scheduler.Termination{Mode: scheduler.Until, Until: start.Add(3 * time.Hour)}}
	first, _, err := scheduler.First(spec)
	if err != nil {
		t.Fatal(err)
	}
	next, more, err := scheduler.Next(spec, first, start.Add(2*time.Hour+30*time.Minute), 1)
	if err != nil || !more || !next.At.Equal(spec.Termination.Until) {
		t.Fatalf("inclusive until=%+v %v %v", next, more, err)
	}
	if _, more, err := scheduler.Next(spec, next, next.At, 2); err != nil || more {
		t.Fatalf("past until=%v %v", more, err)
	}
	spec.Termination = scheduler.Termination{Mode: scheduler.Count, MaxRuns: 2}
	if _, more, err := scheduler.Next(spec, first, first.At, 2); err != nil || more {
		t.Fatalf("max count=%v %v", more, err)
	}
	// An expired rule terminates before elapsed-duration arithmetic, even when
	// its retained calendar anchor is more than a duration's range in the past.
	spec.Schedule.StartAt = time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)
	spec.Termination = scheduler.Termination{Mode: scheduler.Until, Until: spec.Schedule.StartAt.Add(time.Hour)}
	if _, more, err := scheduler.Next(spec, scheduler.Occurrence{At: spec.Schedule.StartAt}, start, 0); err != nil || more {
		t.Fatalf("expired historic rule did not complete: %v %v", more, err)
	}
}

// Property testing is warranted for calendar transitions and count limits.
// The next boundary must move forward without drifting off an interval anchor,
// and a consumed daily local date must never be selected again.
func FuzzScheduleBoundaries(f *testing.F) {
	f.Add(uint16(90), uint16(306), uint8(0))
	f.Add(uint16(150), uint16(68), uint8(1))
	f.Fuzz(func(t *testing.T, minute, day uint16, zone uint8) {
		zones := []string{"America/New_York", "Australia/Lord_Howe", "Asia/Nicosia", "UTC"}
		start := time.Date(2030, 1, 1+int(day%365), 0, 0, 0, 0, time.UTC)
		clock := time.Date(2000, 1, 1, int(minute%1440)/60, int(minute%60), 0, 0, time.UTC).Format("15:04")
		spec := dailySpec(clock, zones[int(zone)%len(zones)], start.Format(time.RFC3339))
		first, _, err := scheduler.First(spec)
		if err != nil {
			t.Fatal(err)
		}
		next, more, err := scheduler.Next(spec, first, first.At.Add(-time.Minute), 0)
		if err != nil || !more || !next.At.After(first.At) || next.Day <= first.Day {
			t.Fatalf("non-monotone calendar: first=%+v next=%+v more=%v err=%v", first, next, more, err)
		}
		spec.Schedule = scheduler.Schedule{Kind: scheduler.Interval, StartAt: start, Every: time.Duration(minute%1440+1) * time.Minute}
		spec.Termination = scheduler.Termination{Mode: scheduler.Count, MaxRuns: int64(day%100 + 1)}
		previous := scheduler.Occurrence{At: start}
		now := start.Add(time.Duration(day%365) * 24 * time.Hour)
		next, more, err = scheduler.Next(spec, previous, now, 0)
		if err != nil || !more || !next.At.After(now) || next.At.Sub(start)%spec.Schedule.Every != 0 {
			t.Fatalf("interval drift: %+v %v %v", next, more, err)
		}
		if _, more, err := scheduler.Next(spec, previous, now, spec.Termination.MaxRuns); err != nil || more {
			t.Fatalf("count escaped: %v %v", more, err)
		}
	})
}
