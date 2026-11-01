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

package domain_test

import (
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// TestParseSpotFundsLimitModeAccepted covers the two wire values.
func TestParseSpotFundsLimitModeAccepted(t *testing.T) {
	t.Parallel()

	for _, want := range []domain.SpotFundsLimitMode{
		domain.SpotFundsLimitModeEnforce,
		domain.SpotFundsLimitModeTrackOnly,
	} {
		t.Run(string(want), func(t *testing.T) {
			t.Parallel()
			got, err := domain.ParseSpotFundsLimitMode(string(want))
			if err != nil {
				t.Fatalf("ParseSpotFundsLimitMode(%q): %v", want, err)
			}
			if got != want {
				t.Fatalf("mode = %q, want %q", got, want)
			}
			if err := domain.ValidateSpotFundsLimitMode(want); err != nil {
				t.Fatalf("ValidateSpotFundsLimitMode(%q): %v", want, err)
			}
		})
	}
}

// TestParseSpotFundsLimitModeRefused covers the refused values: an empty mode
// is never defaulted and an unknown one names both accepted values.
func TestParseSpotFundsLimitModeRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		value      string
		constraint string
		want       []string
	}{
		{"empty", "", "required", []string{"mode is required"}},
		{"unknown", "relaxed", "unsupported_value", []string{
			`"relaxed"`,
			string(domain.SpotFundsLimitModeEnforce),
			string(domain.SpotFundsLimitModeTrackOnly),
		}},
		{"wrong case", "ENFORCE", "unsupported_value", []string{`"ENFORCE"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := domain.ParseSpotFundsLimitMode(tc.value)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ParseSpotFundsLimitMode(%q) error = %v, want ErrInvalid", tc.value, err)
			}
			if got != "" {
				t.Fatalf("mode = %q, want empty on error", got)
			}
			if pointer := domain.ValidationPointer(err); pointer != "/mode" {
				t.Fatalf("ValidationPointer = %q, want %q", pointer, "/mode")
			}
			if constraint := domain.ValidationConstraint(err); constraint != tc.constraint {
				t.Fatalf("ValidationConstraint = %q, want %q", constraint, tc.constraint)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
			validateErr := domain.ValidateSpotFundsLimitMode(domain.SpotFundsLimitMode(tc.value))
			if domain.ValidationConstraint(validateErr) != tc.constraint {
				t.Fatalf("ValidateSpotFundsLimitMode(%q) = %v, want constraint %q", tc.value, validateErr, tc.constraint)
			}
		})
	}
}

// TestSpotFundsLimitModeNoLooserThan pins the one loosening: Enforce to
// TrackOnly. Tightening back and keeping the mode are not loosenings.
func TestSpotFundsLimitModeNoLooserThan(t *testing.T) {
	t.Parallel()

	enforce := domain.SpotFundsLimitModeEnforce
	trackOnly := domain.SpotFundsLimitModeTrackOnly

	err := trackOnly.NoLooserThan(enforce)
	if !errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("track_only.NoLooserThan(enforce) = %v, want ErrLoosening", err)
	}
	for _, want := range []string{string(enforce), string(trackOnly)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("loosening error %q does not name %q", err, want)
		}
	}

	for _, tc := range []struct{ next, prev domain.SpotFundsLimitMode }{
		{enforce, trackOnly},
		{enforce, enforce},
		{trackOnly, trackOnly},
	} {
		if err := tc.next.NoLooserThan(tc.prev); err != nil {
			t.Fatalf("%s.NoLooserThan(%s) = %v, want nil", tc.next, tc.prev, err)
		}
	}
}

// TestSpotFundsLimitModeNoLooserThanFailsClosed pins that a side which is not
// a valid mode is refused as invalid rather than read as Enforce, TrackOnly or
// a loosening: the raw value of an absent key ("") included.
func TestSpotFundsLimitModeNoLooserThanFailsClosed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		next, prev domain.SpotFundsLimitMode
		want       string
	}{
		{"empty current", domain.SpotFundsLimitModeTrackOnly, "", `current mode: mode is required`},
		{"unknown current", domain.SpotFundsLimitModeTrackOnly, "relaxed", `current mode: mode "relaxed"`},
		{"empty next", "", domain.SpotFundsLimitModeEnforce, `mode: mode is required`},
		{"unknown next", "relaxed", domain.SpotFundsLimitModeTrackOnly, `mode: mode "relaxed"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.next.NoLooserThan(tc.prev)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("%q.NoLooserThan(%q) = %v, want ErrInvalid", tc.next, tc.prev, err)
			}
			if errors.Is(err, domain.ErrLoosening) {
				t.Fatalf("%q.NoLooserThan(%q) = %v, want no ErrLoosening", tc.next, tc.prev, err)
			}
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error %q does not start with %s", err, tc.want)
			}
		})
	}
}
