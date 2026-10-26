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

package domain

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNewAuditDetailRendersTemplateAndTail(t *testing.T) {
	t.Parallel()
	d := NewAuditDetail(AuditDetailSetLimit,
		AuditDetailParams{"policy": "rate_limit", "scope": "account"},
		AuditDetailField{Name: "account", Value: "acc-1"},
		AuditDetailField{Name: "max_orders", Value: "100"},
	)
	if want := "Limit set: rate_limit (account) account=acc-1 max_orders=100"; d.Text != want {
		t.Fatalf("Text = %q, want %q", d.Text, want)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestNewAuditDetailKeepsEmptyValuesNil(t *testing.T) {
	t.Parallel()
	d := NewAuditDetail(AuditDetailResetDatabase, AuditDetailParams{})
	if d.Params != nil || d.Fields != nil {
		t.Fatalf("Params = %#v, Fields = %#v, want both nil", d.Params, d.Fields)
	}
	if d.Text != "Database reset from scratch" {
		t.Fatalf("Text = %q", d.Text)
	}
}

func TestNewAuditDetailWithoutRenderingFailsValidate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		detail AuditDetail
	}{
		{"unknown code", NewAuditDetail("no_such_code", nil)},
		{"missing value", NewAuditDetail(AuditDetailCreateAccount, nil)},
		{"extra value", NewAuditDetail(AuditDetailCreateAccount,
			AuditDetailParams{"account": "acc-1", "group": "g"})},
		{"unknown field", NewAuditDetail(AuditDetailCreateAccount,
			AuditDetailParams{"account": "acc-1"}, AuditDetailField{Name: "color", Value: "red"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.detail.Text != "" {
				t.Fatalf("Text = %q, want none", test.detail.Text)
			}
			if err := test.detail.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestAuditDetailValidate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		detail AuditDetail
		valid  bool
	}{
		{"no detail", AuditDetail{}, true},
		{"text without a code", AuditDetail{Text: "Account acc-1 created"}, false},
		{"text that is not the rendering", AuditDetail{
			Code: AuditDetailCreateAccount, Params: AuditDetailParams{"account": "acc-1"},
			Text: "Account acc-2 created",
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.detail.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestAuditDetailValuesMarshalEmptyAsEmptyJSON(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"nil params", AuditDetailParams(nil), `{}`},
		{"params", AuditDetailParams{"account": "acc-1"}, `{"account":"acc-1"}`},
		{"nil fields", AuditDetailFields(nil), `[]`},
		{"fields", AuditDetailFields{{Name: "forced", Value: "true"}}, `[{"name":"forced","value":"true"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("Marshal = %s, want %s", got, test.want)
			}
		})
	}
}

// TestAuditDetailTemplatesMatchPanelCatalog holds the panel's en catalog to
// the templates the server writes the stored text from, so the stored text and
// the en panel read a detail line in one wording.
func TestAuditDetailTemplatesMatchPanelCatalog(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "i18n", "locales", "en", "audit.json"))
	if err != nil {
		t.Fatalf("read en audit catalog: %v", err)
	}
	var catalog struct {
		Detail      map[AuditDetailCode]string `json:"detail"`
		DetailField map[string]string          `json:"detailField"`
		DetailValue map[string]string          `json:"detailValue"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatalf("decode en audit catalog: %v", err)
	}

	for _, code := range slices.Sorted(maps.Keys(auditDetailTemplates)) {
		if got, want := catalog.Detail[code], auditDetailTemplates[code]; got != want {
			t.Errorf("audit:detail.%s = %q, want %q", code, got, want)
		}
	}
	for _, code := range slices.Sorted(maps.Keys(catalog.Detail)) {
		if _, ok := auditDetailTemplates[code]; !ok {
			t.Errorf("audit:detail.%s has no server template", code)
		}
	}

	if got, want := slices.Sorted(maps.Keys(catalog.DetailField)), slices.Sorted(maps.Keys(auditDetailFieldNames)); !slices.Equal(got, want) {
		t.Errorf("audit:detailField names = %v, want %v", got, want)
	}

	// The panel labels the sentinels and the boolean field values; the stored
	// text keeps them as written, like every other code value.
	wantValues := []string{"false", "none", "true", "unset"}
	if got := slices.Sorted(maps.Keys(catalog.DetailValue)); !slices.Equal(got, wantValues) {
		t.Errorf("audit:detailValue keys = %v, want %v", got, wantValues)
	}
}
