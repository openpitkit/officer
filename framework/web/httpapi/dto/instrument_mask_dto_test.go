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

package dto

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"go.openpit.dev/officer/framework/domain"
)

func TestInstrumentMaskDTOJSON(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		dto  InstrumentMaskDTO
		want string
	}{
		{InstrumentMaskDTO{BaseAsset: "BTC", QuoteAsset: "*USD*"}, `{"baseAsset":"BTC","quoteAsset":"*USD*"}`},
		{InstrumentMaskDTO{}, `{"baseAsset":"","quoteAsset":""}`},
	} {
		data, err := json.Marshal(test.dto)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != test.want {
			t.Errorf("JSON = %s, want %s", data, test.want)
		}
	}
}

func TestInstrumentMaskDTOMissingField(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		body, pointer string
	}{
		{`{"baseAsset":"BTC"}`, "/quoteAsset"},
		{`{"quoteAsset":"USD"}`, "/baseAsset"},
		{`{}`, "/baseAsset"},
	} {
		var dto InstrumentMaskDTO
		if err := json.Unmarshal([]byte(test.body), &dto); err != nil {
			t.Fatal(err)
		}
		_, err := FromInstrumentMaskDTO(dto)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("errors.Is(err, ErrInvalid) = false: %v", err)
		}
		if got := domain.ValidationPointer(err); got != test.pointer {
			t.Errorf("ValidationPointer = %q, want %q", got, test.pointer)
		}
		if got := domain.ValidationConstraint(err); got != "required" {
			t.Errorf("ValidationConstraint = %q, want required", got)
		}
	}
}

func TestInstrumentMaskDTORoundTrip(t *testing.T) {
	t.Parallel()
	for _, dto := range []InstrumentMaskDTO{
		{BaseAsset: "b**Tc/", QuoteAsset: "*/uSd*"},
		{BaseAsset: "*", QuoteAsset: "*"},
		{BaseAsset: "é*", QuoteAsset: "USD"},
	} {
		mask, err := FromInstrumentMaskDTO(dto)
		if err != nil {
			t.Fatal(err)
		}
		if got := ToInstrumentMaskDTO(mask); got != dto {
			t.Errorf("round trip = %#v, want %#v", got, dto)
		}
	}
}

func TestInstrumentMaskDTOValidationUnchanged(t *testing.T) {
	t.Parallel()
	for _, dto := range []InstrumentMaskDTO{
		{BaseAsset: "", QuoteAsset: "USD"},
		{BaseAsset: "BTC", QuoteAsset: " B TC"},
	} {
		_, want := domain.ParseInstrumentMask(dto.BaseAsset, dto.QuoteAsset)
		_, got := FromInstrumentMaskDTO(dto)
		if got != want {
			t.Errorf("validation error = %#v, want unchanged %#v", got, want)
		}
	}
}

func TestInstrumentMaskDTOSchemaRequired(t *testing.T) {
	t.Parallel()
	schema, err := jsonschema.For[InstrumentMaskDTO](nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"baseAsset", "quoteAsset"} {
		if !slices.Contains(schema.Required, field) {
			t.Errorf("schema.Required = %v, missing %q", schema.Required, field)
		}
	}
}
