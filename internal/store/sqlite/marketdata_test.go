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
// Please see https://openpit.dev and the OWNERS file for details.

// Market-data group tests: instance create/get/list/update/delete by external
// id; NOCASE label uniqueness conflict; credentials opaque round-trip;
// instrument upsert keyed by (instance, external symbol) with replace-on-
// conflict; unknown asset code error; instance deletion cascade; enabled
// filters for instances and instruments.

package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// seedMDFixtures creates the dictionary rows market-data tests need.
func seedMDFixtures(t *testing.T) (context.Context, RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Apple"}); err != nil {
		t.Fatalf("CreateAsset AAPL: %v", err)
	}
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "USD", Title: "US Dollar"}); err != nil {
		t.Fatalf("CreateAsset USD: %v", err)
	}
	return ctx, rs
}

// sampleInstance returns a fully-populated market-data instance.
func sampleInstance() domain.MarketDataInstance {
	return domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderBYO,
		Label:       "desk-feed",
		Credentials: `{"key":"secret-value"}`,
		Enabled:     false,
	}
}

// TestMDInstanceCreateGetRoundTrip checks that an instance round-trips with
// all fields preserved and the assigned external id has the correct length.
func TestMDInstanceCreateGetRoundTrip(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst := sampleInstance()
	created, err := rs.CreateMarketDataInstance(ctx, inst)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	// External id must be populated and 22 chars.
	if created.ExternalID.IsZero() {
		t.Fatal("CreateMarketDataInstance returned zero external id")
	}
	if got := len(created.ExternalID.String()); got != domain.ExternalIDStringLen {
		t.Fatalf("external id string len = %d, want %d", got, domain.ExternalIDStringLen)
	}

	// All other fields must be preserved.
	if created.Provider != inst.Provider {
		t.Fatalf("Provider = %q, want %q", created.Provider, inst.Provider)
	}
	if created.Label != inst.Label {
		t.Fatalf("Label = %q, want %q", created.Label, inst.Label)
	}
	if created.Credentials != inst.Credentials {
		t.Fatalf("Credentials = %q, want %q", created.Credentials, inst.Credentials)
	}
	if created.Enabled != inst.Enabled {
		t.Fatalf("Enabled = %v, want %v", created.Enabled, inst.Enabled)
	}

	// Get by external id.
	got, ok, err := rs.GetMarketDataInstance(ctx, created.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetMarketDataInstance: ok=%v err=%v", ok, err)
	}
	if got != created {
		t.Fatalf("GetMarketDataInstance = %+v, want %+v", got, created)
	}

	// Get of a missing id returns (_, false, nil).
	other, _ := newExternalID()
	_, ok, err = rs.GetMarketDataInstance(ctx, other)
	if err != nil {
		t.Fatalf("GetMarketDataInstance(missing): unexpected error %v", err)
	}
	if ok {
		t.Fatal("GetMarketDataInstance(missing) returned ok=true")
	}
}

// TestMDInstanceExternalIDOpacity verifies that no surrogate integer id leaks
// through the ListMarketDataInstances result.
func TestMDInstanceExternalIDOpacity(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst := sampleInstance()
	created, err := rs.CreateMarketDataInstance(ctx, inst)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	list, err := rs.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListMarketDataInstances len = %d, want 1", len(list))
	}
	if list[0] != created {
		t.Fatalf("ListMarketDataInstances[0] = %+v, want %+v", list[0], created)
	}
}

// TestMDInstanceLabelNOCASEUniqueness confirms that a duplicate label with
// different casing is rejected with domain.ErrAlreadyExists.
func TestMDInstanceLabelNOCASEUniqueness(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst := sampleInstance()
	if _, err := rs.CreateMarketDataInstance(ctx, inst); err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	// Same label, all upper-case — must conflict.
	dup := inst
	dup.Label = "DESK-FEED"
	_, err := rs.CreateMarketDataInstance(ctx, dup)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateMarketDataInstance(dup label) error = %v, want ErrAlreadyExists", err)
	}
}

// TestMDInstanceCredentialsOpaqueRoundTrip verifies the credentials blob is
// returned byte-for-byte without any interpretation.
func TestMDInstanceCredentialsOpaqueRoundTrip(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	raw := `{"user":"alice","pass":"p@ss word","nested":{"x":1}}`
	inst := domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderIB,
		Label:       "ib-prod",
		Credentials: raw,
	}
	created, err := rs.CreateMarketDataInstance(ctx, inst)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	got, _, err := rs.GetMarketDataInstance(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetMarketDataInstance: %v", err)
	}
	if got.Credentials != raw {
		t.Fatalf("Credentials round-trip mismatch:\ngot  %q\nwant %q", got.Credentials, raw)
	}
}

// TestMDInstanceListAll covers ListMarketDataInstances ordering.
func TestMDInstanceListAll(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	// Create two instances.
	a, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance,
		Label:    "feed-a",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance a: %v", err)
	}
	b, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderKraken,
		Label:    "feed-b",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance b: %v", err)
	}

	list, err := rs.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListMarketDataInstances len = %d, want 2", len(list))
	}
	// Both instances present.
	ids := map[domain.ExternalID]bool{a.ExternalID: true, b.ExternalID: true}
	for _, inst := range list {
		if !ids[inst.ExternalID] {
			t.Fatalf("unexpected instance in list: %+v", inst)
		}
	}
}

// TestMDInstanceSetEnabled toggles the enabled flag.
func TestMDInstanceSetEnabled(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	created, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if created.Enabled {
		t.Fatal("instance should start disabled")
	}

	if err := rs.SetMarketDataInstanceEnabled(ctx, created.ExternalID, true); err != nil {
		t.Fatalf("SetMarketDataInstanceEnabled(true): %v", err)
	}
	got, _, err := rs.GetMarketDataInstance(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetMarketDataInstance: %v", err)
	}
	if !got.Enabled {
		t.Fatal("instance should be enabled after toggle")
	}

	// ListEnabledMarketDataInstances must now return it.
	enabled, err := rs.ListEnabledMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListEnabledMarketDataInstances: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ExternalID != created.ExternalID {
		t.Fatalf("ListEnabledMarketDataInstances = %v, want [%v]", enabled, created.ExternalID)
	}

	// Toggle back off.
	if err := rs.SetMarketDataInstanceEnabled(ctx, created.ExternalID, false); err != nil {
		t.Fatalf("SetMarketDataInstanceEnabled(false): %v", err)
	}
	enabled, err = rs.ListEnabledMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListEnabledMarketDataInstances after disable: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("ListEnabledMarketDataInstances len = %d, want 0", len(enabled))
	}
}

// TestMDInstanceSetEnabledNotFound confirms ErrNotFound for a missing instance.
func TestMDInstanceSetEnabledNotFound(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	ghost, _ := newExternalID()
	err := rs.SetMarketDataInstanceEnabled(ctx, ghost, true)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetMarketDataInstanceEnabled(missing) error = %v, want ErrNotFound", err)
	}
}

// TestMDInstanceUpdateSettings replaces label and credentials.
func TestMDInstanceUpdateSettings(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	created, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	newLabel := "renamed-feed"
	newCreds := `{"token":"new-token"}`
	if err := rs.UpdateMarketDataInstanceSettings(
		ctx, created.ExternalID, newLabel, newCreds,
	); err != nil {
		t.Fatalf("UpdateMarketDataInstanceSettings: %v", err)
	}

	got, _, err := rs.GetMarketDataInstance(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetMarketDataInstance after update: %v", err)
	}
	if got.Label != newLabel {
		t.Fatalf("Label = %q, want %q", got.Label, newLabel)
	}
	if got.Credentials != newCreds {
		t.Fatalf("Credentials = %q, want %q", got.Credentials, newCreds)
	}
	// Provider and enabled must be unchanged.
	if got.Provider != created.Provider {
		t.Fatalf("Provider changed: got %q, want %q", got.Provider, created.Provider)
	}
	if got.Enabled != created.Enabled {
		t.Fatalf("Enabled changed: got %v, want %v", got.Enabled, created.Enabled)
	}
}

// TestMDInstanceUpdateSettingsLabelConflict checks that a duplicate label on
// update returns ErrAlreadyExists.
func TestMDInstanceUpdateSettingsLabelConflict(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	a, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO,
		Label:    "label-a",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance a: %v", err)
	}
	b, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO,
		Label:    "label-b",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance b: %v", err)
	}

	// Try to rename b to "LABEL-A" (case-insensitive conflict with a).
	err = rs.UpdateMarketDataInstanceSettings(ctx, b.ExternalID, "LABEL-A", "")
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("UpdateMarketDataInstanceSettings(dup label) error = %v, want ErrAlreadyExists", err)
	}

	// a's label must be unchanged.
	gotA, _, err := rs.GetMarketDataInstance(ctx, a.ExternalID)
	if err != nil {
		t.Fatalf("GetMarketDataInstance a: %v", err)
	}
	if gotA.Label != "label-a" {
		t.Fatalf("label-a was mutated: got %q", gotA.Label)
	}
}

// TestMDInstanceUpdateSettingsNotFound confirms ErrNotFound for a missing
// instance.
func TestMDInstanceUpdateSettingsNotFound(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	ghost, _ := newExternalID()
	err := rs.UpdateMarketDataInstanceSettings(ctx, ghost, "label", "creds")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateMarketDataInstanceSettings(missing) error = %v, want ErrNotFound", err)
	}
}

// TestMDInstanceDelete removes an instance and verifies absence.
func TestMDInstanceDelete(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	created, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	if err := rs.DeleteMarketDataInstance(ctx, created.ExternalID); err != nil {
		t.Fatalf("DeleteMarketDataInstance: %v", err)
	}

	_, ok, err := rs.GetMarketDataInstance(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetMarketDataInstance after delete: %v", err)
	}
	if ok {
		t.Fatal("GetMarketDataInstance after delete returned ok=true")
	}

	// Delete of a missing instance is ErrNotFound.
	if err := rs.DeleteMarketDataInstance(ctx, created.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteMarketDataInstance(missing) error = %v, want ErrNotFound", err)
	}
}

// --- Instruments ------------------------------------------------------------

// sampleInstrument returns an instrument for a given instance external id.
func sampleInstrument(instance domain.ExternalID) domain.MarketDataInstrument {
	return domain.MarketDataInstrument{
		Instance:       instance,
		ExternalSymbol: "AAPL",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Enabled:        false,
		ManualPrice:    "150.00",
	}
}

// TestMDInstrumentUpsertAndList verifies basic upsert and list round-trip.
func TestMDInstrumentUpsertAndList(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	instr := sampleInstrument(inst.ExternalID)
	if err := rs.UpsertMarketDataInstrument(ctx, instr); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}

	list, err := rs.ListMarketDataInstruments(ctx, inst.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListMarketDataInstruments len = %d, want 1", len(list))
	}
	got := list[0]
	if got.Instance != inst.ExternalID {
		t.Fatalf("Instance = %v, want %v", got.Instance, inst.ExternalID)
	}
	if got.ExternalSymbol != instr.ExternalSymbol {
		t.Fatalf("ExternalSymbol = %q, want %q", got.ExternalSymbol, instr.ExternalSymbol)
	}
	if got.BaseAsset != instr.BaseAsset {
		t.Fatalf("BaseAsset = %q, want %q", got.BaseAsset, instr.BaseAsset)
	}
	if got.QuoteAsset != instr.QuoteAsset {
		t.Fatalf("QuoteAsset = %q, want %q", got.QuoteAsset, instr.QuoteAsset)
	}
	base, ok, err := rs.GetAsset(ctx, instr.BaseAsset)
	if err != nil || !ok {
		t.Fatalf("GetAsset(%s): ok=%v err=%v", instr.BaseAsset, ok, err)
	}
	quote, ok, err := rs.GetAsset(ctx, instr.QuoteAsset)
	if err != nil || !ok {
		t.Fatalf("GetAsset(%s): ok=%v err=%v", instr.QuoteAsset, ok, err)
	}
	if got.BaseAssetID != base.EngineAssetID || got.QuoteAssetID != quote.EngineAssetID {
		t.Fatalf(
			"asset ids = %d/%d, want %d/%d",
			got.BaseAssetID, got.QuoteAssetID, base.EngineAssetID, quote.EngineAssetID,
		)
	}
	if got.ManualPrice != instr.ManualPrice {
		t.Fatalf("ManualPrice = %q, want %q", got.ManualPrice, instr.ManualPrice)
	}
	if got.Enabled != instr.Enabled {
		t.Fatalf("Enabled = %v, want %v", got.Enabled, instr.Enabled)
	}
}

// TestMDInstrumentUpsertReplaceOnConflict confirms that upserting the same
// (instance, externalSymbol) key replaces the mutable columns.
func TestMDInstrumentUpsertReplaceOnConflict(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	// Add a second base asset to replace with.
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "BTC"}); err != nil {
		t.Fatalf("CreateAsset BTC: %v", err)
	}

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	instr := sampleInstrument(inst.ExternalID)
	if err := rs.UpsertMarketDataInstrument(ctx, instr); err != nil {
		t.Fatalf("UpsertMarketDataInstrument (first): %v", err)
	}

	// Re-upsert with different base asset, enabled=true, and no manual price.
	updated := instr
	updated.BaseAsset = "BTC"
	updated.Enabled = true
	updated.ManualPrice = ""
	if err := rs.UpsertMarketDataInstrument(ctx, updated); err != nil {
		t.Fatalf("UpsertMarketDataInstrument (second): %v", err)
	}

	list, err := rs.ListMarketDataInstruments(ctx, inst.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListMarketDataInstruments len = %d, want 1 after upsert", len(list))
	}
	got := list[0]
	if got.BaseAsset != "BTC" {
		t.Fatalf("BaseAsset after replace = %q, want BTC", got.BaseAsset)
	}
	if !got.Enabled {
		t.Fatal("Enabled after replace should be true")
	}
	if got.ManualPrice != "" {
		t.Fatalf("ManualPrice after replace = %q, want empty", got.ManualPrice)
	}
}

// TestMDInstrumentUpsertUnknownAsset confirms ErrInvalid for an unknown asset
// code.
func TestMDInstrumentUpsertUnknownAsset(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	bad := domain.MarketDataInstrument{
		Instance:       inst.ExternalID,
		ExternalSymbol: "XYZ",
		BaseAsset:      "UNKNOWN",
		QuoteAsset:     "USD",
	}
	err = rs.UpsertMarketDataInstrument(ctx, bad)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("UpsertMarketDataInstrument(unknown asset) error = %v, want ErrInvalid", err)
	}
}

// TestMDInstrumentSetEnabled toggles the enabled flag and checks the enabled
// filter.
func TestMDInstrumentSetEnabled(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	instr := sampleInstrument(inst.ExternalID)
	if err := rs.UpsertMarketDataInstrument(ctx, instr); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}

	// Enable it.
	if err := rs.SetMarketDataInstrumentEnabled(
		ctx, inst.ExternalID, instr.ExternalSymbol, true,
	); err != nil {
		t.Fatalf("SetMarketDataInstrumentEnabled(true): %v", err)
	}

	// ListEnabledMarketDataInstruments must include it.
	enabled, err := rs.ListEnabledMarketDataInstruments(ctx, inst.ExternalID)
	if err != nil {
		t.Fatalf("ListEnabledMarketDataInstruments: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ExternalSymbol != instr.ExternalSymbol {
		t.Fatalf("ListEnabledMarketDataInstruments = %v, want [AAPL]", enabled)
	}

	// Disable.
	if err := rs.SetMarketDataInstrumentEnabled(
		ctx, inst.ExternalID, instr.ExternalSymbol, false,
	); err != nil {
		t.Fatalf("SetMarketDataInstrumentEnabled(false): %v", err)
	}
	enabled, err = rs.ListEnabledMarketDataInstruments(ctx, inst.ExternalID)
	if err != nil {
		t.Fatalf("ListEnabledMarketDataInstruments after disable: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("ListEnabledMarketDataInstruments len = %d, want 0", len(enabled))
	}
}

// TestMDInstrumentSetEnabledNotFound confirms ErrNotFound for a missing
// instrument.
func TestMDInstrumentSetEnabledNotFound(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	err = rs.SetMarketDataInstrumentEnabled(ctx, inst.ExternalID, "GHOST", true)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetMarketDataInstrumentEnabled(missing) error = %v, want ErrNotFound", err)
	}
}

// TestMDInstrumentDelete removes one instrument and verifies it is gone while
// the instance remains.
func TestMDInstrumentDelete(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	instr := sampleInstrument(inst.ExternalID)
	if err := rs.UpsertMarketDataInstrument(ctx, instr); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}

	if err := rs.DeleteMarketDataInstrument(
		ctx, inst.ExternalID, instr.ExternalSymbol,
	); err != nil {
		t.Fatalf("DeleteMarketDataInstrument: %v", err)
	}

	list, err := rs.ListMarketDataInstruments(ctx, inst.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("ListMarketDataInstruments len = %d, want 0", len(list))
	}

	// Delete of a missing instrument is ErrNotFound.
	err = rs.DeleteMarketDataInstrument(ctx, inst.ExternalID, instr.ExternalSymbol)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteMarketDataInstrument(missing) error = %v, want ErrNotFound", err)
	}
}

// TestMDCascadeDeleteInstance confirms that deleting an instance removes its
// instruments without removing their global assets.
func TestMDCascadeDeleteInstance(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	instr := sampleInstrument(inst.ExternalID)
	if err := rs.UpsertMarketDataInstrument(ctx, instr); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	// Delete the instance.
	if err := rs.DeleteMarketDataInstance(ctx, inst.ExternalID); err != nil {
		t.Fatalf("DeleteMarketDataInstance: %v", err)
	}

	// Instruments for that instance must be gone.
	instrList, err := rs.ListMarketDataInstruments(ctx, inst.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments after cascade: %v", err)
	}
	if len(instrList) != 0 {
		t.Fatalf("instruments not cascaded: got %d rows, want 0", len(instrList))
	}

	// Feed-owned rows must not remove the global assets they reference.
	for _, code := range []string{"AAPL", "USD"} {
		if _, ok, err := rs.GetAsset(ctx, code); err != nil {
			t.Fatalf("GetAsset %s after source delete: %v", code, err)
		} else if !ok {
			t.Fatalf("asset %s was removed with its market-data source", code)
		}
	}
}

// TestMDListInstrumentsEmptyInstance confirms a non-nil empty slice when the
// instance has no instruments.
func TestMDListInstrumentsEmptyInstance(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst, err := rs.CreateMarketDataInstance(ctx, sampleInstance())
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	list, err := rs.ListMarketDataInstruments(ctx, inst.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if list == nil {
		t.Fatal("ListMarketDataInstruments returned nil, want non-nil empty slice")
	}
	if len(list) != 0 {
		t.Fatalf("ListMarketDataInstruments len = %d, want 0", len(list))
	}
}

// TestMDInstanceSuppliedExternalIDUsedVerbatim verifies that a caller-supplied
// external id is written verbatim: the returned id equals the supplied one and
// the row is addressable by it.
func TestMDInstanceSuppliedExternalIDUsedVerbatim(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	supplied := domain.ExternalID("caller-market-data-1")
	inst := sampleInstance()
	inst.ExternalID = supplied

	created, err := rs.CreateMarketDataInstance(ctx, inst)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance(supplied id): %v", err)
	}
	if created.ExternalID != supplied {
		t.Fatalf("returned id = %q, want supplied %q",
			created.ExternalID.String(), supplied.String())
	}

	got, ok, err := rs.GetMarketDataInstance(ctx, supplied)
	if err != nil || !ok {
		t.Fatalf("GetMarketDataInstance(supplied id): ok=%v err=%v", ok, err)
	}
	if got.ExternalID != supplied {
		t.Fatalf("row external id = %q, want %q",
			got.ExternalID.String(), supplied.String())
	}
}

// TestMDInstanceDuplicateSuppliedExternalIDConflicts verifies that a second
// create with the same supplied external id is rejected with
// domain.ErrAlreadyExists naming the id (not the label, even when the labels
// differ), and that no second row is written.
func TestMDInstanceDuplicateSuppliedExternalIDConflicts(t *testing.T) {
	ctx, rs := seedMDFixtures(t)
	r := rs.(*realmStore)

	supplied := domain.ExternalID("caller-market-data-1")
	first := sampleInstance()
	first.ExternalID = supplied
	if _, err := rs.CreateMarketDataInstance(ctx, first); err != nil {
		t.Fatalf("CreateMarketDataInstance(first): %v", err)
	}

	// A distinct label isolates the external_id constraint from the label
	// constraint: the conflict must come from the duplicated id.
	second := sampleInstance()
	second.ExternalID = supplied
	second.Label = "another-feed"
	_, err := rs.CreateMarketDataInstance(ctx, second)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateMarketDataInstance(dup id) = %v, want ErrAlreadyExists", err)
	}
	if msg := err.Error(); !strings.Contains(msg, supplied.String()) {
		t.Fatalf("conflict error %q does not name the external id %q",
			msg, supplied.String())
	}

	if n := countRows(t, ctx, r, "market_data_instance"); n != 1 {
		t.Fatalf("instance row count = %d, want 1 (no second row)", n)
	}
}

// TestMDInstanceGeneratesExternalIDWhenAbsent verifies that a zero supplied id
// is replaced by a freshly generated 22-char external id.
func TestMDInstanceGeneratesExternalIDWhenAbsent(t *testing.T) {
	ctx, rs := seedMDFixtures(t)

	inst := sampleInstance()
	if !inst.ExternalID.IsZero() {
		t.Fatal("sample instance must carry a zero external id")
	}
	created, err := rs.CreateMarketDataInstance(ctx, inst)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if created.ExternalID.IsZero() {
		t.Fatal("CreateMarketDataInstance returned a zero external id")
	}
	if got := len(created.ExternalID.String()); got != domain.ExternalIDStringLen {
		t.Fatalf("generated id len = %d, want %d", got, domain.ExternalIDStringLen)
	}
}
