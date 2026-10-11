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

package health

import (
	"context"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"
)

// capturingHandler keeps every record its logger emits, in emit order.
type capturingHandler struct {
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingHandler) WithGroup(string) slog.Handler { return h }

// recordAttrs returns r's attributes as "key=value" strings in emit order.
func recordAttrs(r slog.Record) []string {
	var attrs []string
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a.String())
		return true
	})
	return attrs
}

func TestRecordStampsZeroAtInUTC(t *testing.T) {
	t.Parallel()
	got := Record(
		slog.New(&capturingHandler{}), nil, Diagnostic{Code: CodeNoData},
	)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].At.IsZero() {
		t.Fatal("zero At was not stamped")
	}
	if loc := got[0].At.Location(); loc != time.UTC {
		t.Fatalf("stamped At location = %v, want UTC", loc)
	}
}

func TestRecordKeepsNonZeroAt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("UTC+1", 3600))
	got := Record(
		slog.New(&capturingHandler{}), nil,
		Diagnostic{Code: CodeNoData, At: at},
	)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !got[0].At.Equal(at) || got[0].At.Location() != at.Location() {
		t.Fatalf("At = %v, want the caller's %v kept", got[0].At, at)
	}
}

func TestRecordRepeatRefreshesLastAtInPlace(t *testing.T) {
	t.Parallel()
	t.Run("explicit At", func(t *testing.T) {
		t.Parallel()
		h := &capturingHandler{}
		logger := slog.New(h)
		first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		later := first.Add(time.Minute)
		a := Diagnostic{
			Level:  DiagError,
			Code:   CodeConnectionError,
			Title:  "Connection failed",
			Detail: "dial refused",
			At:     first,
		}
		b := Diagnostic{
			Level:      DiagWarn,
			Code:       CodeNoData,
			Title:      "No market data",
			Instrument: "BTC/USD",
			Detail:     "no quotes arriving",
			At:         first,
		}
		diags := Record(logger, nil, a)
		diags = Record(logger, diags, b)
		h.records = nil

		// Same dedupe key as b; Level and Title lie outside it.
		repeat := b
		repeat.Level = DiagError
		repeat.Title = "Market data lost"
		repeat.At = later
		got := Record(logger, diags, repeat)

		if len(got) != len(diags) {
			t.Fatalf("len = %d, want %d: a repeat must not grow the buffer",
				len(got), len(diags))
		}
		if !diags[1].At.Equal(later) {
			t.Fatalf("caller's last At = %v, want refreshed to %v",
				diags[1].At, later)
		}
		if !reflect.DeepEqual(diags[0], a) {
			t.Fatalf("diags[0] = %+v, want untouched %+v", diags[0], a)
		}
		if diags[1].Level != b.Level || diags[1].Title != b.Title {
			t.Fatalf("stored Level, Title = %q, %q, want original %q, %q",
				diags[1].Level, diags[1].Title, b.Level, b.Title)
		}
		if len(h.records) != 0 {
			t.Fatalf("repeat logged %d records, want none", len(h.records))
		}
	})
	t.Run("zero At", func(t *testing.T) {
		t.Parallel()
		h := &capturingHandler{}
		logger := slog.New(h)
		stored := Diagnostic{
			Level: DiagWarn, Code: CodeNoData, Title: "No market data",
			At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		diags := Record(logger, nil, stored)
		h.records = nil

		repeat := stored
		repeat.At = time.Time{}
		before := time.Now()
		got := Record(logger, diags, repeat)

		if len(got) != len(diags) {
			t.Fatalf("len = %d, want %d: a repeat must not grow the buffer",
				len(got), len(diags))
		}
		at := diags[len(diags)-1].At
		if at.IsZero() {
			t.Fatal("caller's last At is zero, want stamped by the repeat")
		}
		if loc := at.Location(); loc != time.UTC {
			t.Fatalf("caller's last At location = %v, want UTC", loc)
		}
		if at.Before(before) {
			t.Fatalf("caller's last At = %v, want not earlier than %v",
				at, before)
		}
		if len(h.records) != 0 {
			t.Fatalf("repeat logged %d records, want none", len(h.records))
		}
	})
}

func TestRecordAlternatingDiagnosticsAreEachRecorded(t *testing.T) {
	t.Parallel()
	a := Diagnostic{
		Code:       CodeNoData,
		Instrument: "BTC/USD",
		Detail:     "no quotes arriving",
	}
	withCode, withInstrument, withDetail := a, a, a
	withCode.Code = CodeDataFreshness
	withInstrument.Instrument = "ETH/USD"
	withDetail.Detail = "quotes stale"
	cases := []struct {
		name string
		b    Diagnostic
	}{
		{name: "code differs", b: withCode},
		{name: "instrument differs", b: withInstrument},
		{name: "detail differs", b: withDetail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &capturingHandler{}
			logger := slog.New(h)
			var diags []Diagnostic
			for _, diag := range []Diagnostic{a, tc.b, a} {
				diags = Record(logger, diags, diag)
			}
			if len(diags) != 3 {
				t.Fatalf("len = %d, want 3: A, B, A must each be recorded",
					len(diags))
			}
			if len(h.records) != 3 {
				t.Fatalf("logged %d records, want 3", len(h.records))
			}
		})
	}
}

func TestRecordKeepsNewestTwenty(t *testing.T) {
	t.Parallel()
	logger := slog.New(&capturingHandler{})
	var diags []Diagnostic
	for i := range 21 {
		diags = Record(
			logger, diags, Diagnostic{Code: CodeNoData, Detail: strconv.Itoa(i)},
		)
	}
	if len(diags) != 20 {
		t.Fatalf("len = %d, want 20", len(diags))
	}
	for i, diag := range diags {
		if want := strconv.Itoa(i + 1); diag.Detail != want {
			t.Fatalf(
				"diags[%d].Detail = %q, want %q: oldest dropped, order kept",
				i, diag.Detail, want,
			)
		}
	}
}

func TestRecordLogsTitleWithOrderedAttrs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		diag Diagnostic
		want []string
	}{
		{
			name: "instrument and detail",
			diag: Diagnostic{
				Level:      DiagWarn,
				Code:       CodeNoData,
				Title:      "No market data",
				Instrument: "BTC/USD",
				Detail:     "no quotes arriving",
			},
			want: []string{
				"instance=inst-1",
				"provider=mock",
				"code=no_data",
				"instrument=BTC/USD",
				"detail=no quotes arriving",
			},
		},
		{
			name: "source-wide without detail",
			diag: Diagnostic{
				Level: DiagWarn,
				Code:  CodeNoData,
				Title: "No market data",
			},
			want: []string{
				"instance=inst-1",
				"provider=mock",
				"code=no_data",
			},
		},
		{
			name: "source-wide with detail",
			diag: Diagnostic{
				Level:  DiagWarn,
				Code:   CodeNoData,
				Title:  "No market data",
				Detail: "no quotes arriving",
			},
			want: []string{
				"instance=inst-1",
				"provider=mock",
				"code=no_data",
				"detail=no quotes arriving",
			},
		},
		{
			name: "instrument without detail",
			diag: Diagnostic{
				Level:      DiagWarn,
				Code:       CodeNoData,
				Title:      "No market data",
				Instrument: "BTC/USD",
			},
			want: []string{
				"instance=inst-1",
				"provider=mock",
				"code=no_data",
				"instrument=BTC/USD",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &capturingHandler{}
			Record(
				slog.New(h), nil, tc.diag,
				"instance", "inst-1", "provider", "mock",
			)
			if len(h.records) != 1 {
				t.Fatalf("logged %d records, want 1", len(h.records))
			}
			if msg := h.records[0].Message; msg != tc.diag.Title {
				t.Fatalf("message = %q, want Title %q", msg, tc.diag.Title)
			}
			if got := recordAttrs(h.records[0]); !slices.Equal(got, tc.want) {
				t.Fatalf("attrs = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRecordLogLevels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		diag Diagnostic
		want slog.Level
	}{
		{
			name: "routine error",
			diag: Diagnostic{Routine: true, Level: DiagError, Code: CodeNoData},
			want: slog.LevelDebug,
		},
		{
			name: "error",
			diag: Diagnostic{Level: DiagError, Code: CodeNoData},
			want: slog.LevelError,
		},
		{
			name: "warn",
			diag: Diagnostic{Level: DiagWarn, Code: CodeNoData},
			want: slog.LevelWarn,
		},
		{
			name: "info",
			diag: Diagnostic{Level: DiagInfo, Code: CodeNoData},
			want: slog.LevelInfo,
		},
		{
			name: "other",
			diag: Diagnostic{Level: "notice", Code: CodeNoData},
			want: slog.LevelInfo,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &capturingHandler{}
			Record(slog.New(h), nil, tc.diag)
			if len(h.records) != 1 {
				t.Fatalf("logged %d records, want 1", len(h.records))
			}
			if got := h.records[0].Level; got != tc.want {
				t.Fatalf("level = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRecordDoesNotWriteCallerAttrs(t *testing.T) {
	t.Parallel()
	const sentinel = "untouched"
	backing := make([]any, 8)
	for i := range backing {
		backing[i] = sentinel
	}
	backing[0], backing[1] = "instance", "inst-1"
	Record(
		slog.New(&capturingHandler{}), nil,
		Diagnostic{
			Code:       CodeNoData,
			Instrument: "BTC/USD",
			Detail:     "no quotes arriving",
		},
		backing[:2]...,
	)
	for i, v := range backing[2:] {
		if v != sentinel {
			t.Fatalf("attrs backing[%d] = %v, want %q: Record wrote the "+
				"caller's array", i+2, v, sentinel)
		}
	}
}
