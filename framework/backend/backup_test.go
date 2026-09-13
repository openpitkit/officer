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

package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

const (
	backupRestoreBaseAssetID  domain.EngineAssetID = 101
	backupRestoreQuoteAssetID domain.EngineAssetID = 202
)

type backupRestoreTestNode struct {
	node.Node

	instances          []domain.MarketDataInstance
	instruments        []domain.MarketDataInstrument
	removeAfterRestore bool

	// restoreCalls and restoredArchive record what RestoreBackup actually
	// received, so tests can assert the routed call rather than infer it.
	restoreCalls    int
	restoredArchive backup.Archive
}

func (n *backupRestoreTestNode) ListMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	return append([]domain.MarketDataInstance(nil), n.instances...), nil
}

func (n *backupRestoreTestNode) ListMarketDataInstruments(
	_ context.Context,
	instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	var instruments []domain.MarketDataInstrument
	for _, instrument := range n.instruments {
		if instrument.Instance == instance {
			instruments = append(instruments, instrument)
		}
	}
	return instruments, nil
}

func (n *backupRestoreTestNode) RestoreBackup(
	_ context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
	_ domain.Caller,
) (backup.RestoreSummary, marketdata.Sink, error) {
	if opts.ValidateMarketDataInstance != nil {
		for _, archived := range archive.Data.MarketDataInstances {
			if err := opts.ValidateMarketDataInstance(domain.MarketDataInstance{
				ExternalID: archived.ExternalID, Provider: archived.Provider,
				Label: archived.Label, Credentials: string(archived.Credentials),
				Enabled: archived.Enabled,
			}); err != nil {
				return backup.RestoreSummary{}, nil, fmt.Errorf(
					"store: restore market-data instance %q: %w",
					archived.ExternalID, err,
				)
			}
		}
	}
	n.restoreCalls++
	n.restoredArchive = archive
	if n.removeAfterRestore {
		n.instruments = nil
		return backup.RestoreSummary{}, nil, nil
	}
	for _, archived := range archive.Data.MarketDataInstruments {
		for i := range n.instruments {
			stored := &n.instruments[i]
			if stored.Instance != archived.Instance ||
				stored.ExternalSymbol != archived.ExternalSymbol {
				continue
			}
			stored.BaseAsset = archived.BaseAsset
			stored.QuoteAsset = archived.QuoteAsset
			stored.ManualPrice = archived.ManualPrice
			stored.Enabled = archived.Enabled
		}
	}
	return backup.RestoreSummary{}, nil, nil
}

type backupRestoreTestRuntime struct {
	MarketDataRuntime

	stops    int
	restarts int
	pushed   []domain.MarketDataInstrument
}

type backupRestoreTestConnector struct{}

type backupRestoreTestSigner struct {
	fwsigning.Service
	reloads   int
	reloadErr error
}

func (s *backupRestoreTestSigner) Reload(context.Context) error {
	s.reloads++
	return s.reloadErr
}

func (backupRestoreTestConnector) Subscribe(
	context.Context, []marketdata.Subscription,
) (<-chan marketdata.QuoteUpdate, error) {
	updates := make(chan marketdata.QuoteUpdate)
	close(updates)
	return updates, nil
}

func (backupRestoreTestConnector) Close() {}

func (r *backupRestoreTestRuntime) Stop() {
	r.stops++
}

func (r *backupRestoreTestRuntime) Restart() error {
	r.restarts++
	return nil
}

func (r *backupRestoreTestRuntime) UseSink(marketdata.Sink) error {
	return nil
}

func (r *backupRestoreTestRuntime) PushManual(
	_ context.Context,
	_ string,
	instrument domain.MarketDataInstrument,
) error {
	r.pushed = append(r.pushed, instrument)
	return nil
}

func newBackupRestoreTestService(
	t *testing.T,
	removeAfterRestore bool,
) (*Service, *backupRestoreTestNode, *backupRestoreTestRuntime) {
	t.Helper()
	instance := domain.MarketDataInstance{
		ExternalID: "manual-feed",
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}
	n := &backupRestoreTestNode{
		instances: []domain.MarketDataInstance{instance},
		instruments: []domain.MarketDataInstrument{{
			Instance:       instance.ExternalID,
			ExternalSymbol: "AAPL-USD",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			BaseAssetID:    backupRestoreBaseAssetID,
			QuoteAssetID:   backupRestoreQuoteAssetID,
			ManualPrice:    "100",
			Enabled:        true,
		}},
		removeAfterRestore: removeAfterRestore,
	}
	md := &backupRestoreTestRuntime{}
	registry := marketdata.NewRegistry()
	if err := registry.Register(marketdata.Provider{
		Type:  domain.MarketDataProviderBYO,
		Title: "BYO",
		Build: func(domain.MarketDataInstance) (marketdata.Connector, error) {
			return backupRestoreTestConnector{}, nil
		},
	}); err != nil {
		t.Fatalf("register BYO provider: %v", err)
	}
	// New guarantees signer is never a nil interface; a literal built here has
	// to uphold that invariant itself.
	return &Service{
		node:     n,
		md:       md,
		registry: registry,
		signer:   fwsigning.ServiceOrUnavailable(nil),
	}, n, md
}

func backupRestoreTestArchive(
	manualPrice string,
) (backup.Archive, backup.RestoreOptions) {
	archive := backup.Archive{
		CredentialForm: backup.CredentialFormPlaintext,
		Manifest: backup.Manifest{
			Source:   "test",
			Sections: []backup.Section{backup.SectionMarketData},
		},
		Data: backup.Data{
			MarketDataInstances: []backup.MarketDataInstance{{
				ExternalID: "manual-feed",
				Provider:   domain.MarketDataProviderBYO,
				Enabled:    true,
			}},
			MarketDataInstruments: []backup.MarketDataInstrument{{
				Instance:       "manual-feed",
				ExternalSymbol: "AAPL-USD",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				ManualPrice:    manualPrice,
				Enabled:        true,
			}},
		},
	}
	opts := backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{backup.SectionMarketData},
		},
		Mode: backup.RestoreModeOverwrite,
	}
	return archive, opts
}

func TestRestoreBackupInstallsRegistryBackedMarketDataValidator(t *testing.T) {
	t.Parallel()

	svc, _, _ := newBackupRestoreTestService(t, false)
	archive, opts := backupRestoreTestArchive("100")
	archive.Data.MarketDataInstances[0].Provider = "unknown"

	_, err := svc.RestoreBackup(context.Background(), archive, opts)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup error = %v, want ErrInvalid", err)
	}
	for _, want := range []string{"manual-feed", `provider "unknown"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
		}
	}
}

func TestRestoreBackupRegistryRejectedInstanceFailsRestore(t *testing.T) {
	t.Parallel()

	svc, _, _ := newBackupRestoreTestService(t, false)
	provider, _ := svc.registry.Lookup(domain.MarketDataProviderBYO)
	provider.Build = func(instance domain.MarketDataInstance) (marketdata.Connector, error) {
		if instance.Credentials == `{"token":"reject"}` {
			return nil, errors.New("rejected provider credentials")
		}
		return backupRestoreTestConnector{}, nil
	}
	if err := svc.registry.Register(provider); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	archive, opts := backupRestoreTestArchive("100")
	archive.Data.MarketDataInstances[0].Credentials = []byte(`{"token":"reject"}`)

	_, err := svc.RestoreBackup(context.Background(), archive, opts)
	if !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "rejected provider credentials") {
		t.Fatalf("RestoreBackup error = %v, want provider credential rejection", err)
	}
}

func TestRestoreBackupRejectsMissingOrUnknownCredentialFormBeforeNode(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		form string
	}{
		{name: "missing"},
		{name: "unknown", form: "future"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			svc, n, _ := newBackupRestoreTestService(t, false)
			archive, opts := backupRestoreTestArchive("100")
			archive.CredentialForm = test.form
			if _, err := svc.RestoreBackup(context.Background(), archive, opts); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup credential form %q = %v, want ErrInvalid", test.form, err)
			}
			if n.restoreCalls != 0 {
				t.Fatalf("restore reached node with credential form %q", test.form)
			}
		})
	}
}

func TestRestoreBackupReloadsSignerFromCommittedStore(t *testing.T) {
	t.Parallel()

	svc, _, _ := newBackupRestoreTestService(t, false)
	signer := &backupRestoreTestSigner{}
	svc.signer = signer
	archive, opts := backupRestoreTestArchive("100")
	if _, err := svc.RestoreBackup(context.Background(), archive, opts); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if signer.reloads != 1 {
		t.Fatalf("signer reloads = %d, want 1", signer.reloads)
	}
}

func TestRestoreBackupManualPricePushesCommittedInstrument(t *testing.T) {
	t.Parallel()

	svc, _, md := newBackupRestoreTestService(t, false)
	archive, opts := backupRestoreTestArchive("125.5")
	if _, err := svc.RestoreBackup(context.Background(), archive, opts); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	assertBackupRestoreManualPush(t, md, "125.5")
}

func TestRestoreBackupManualPriceClearPushesCommittedInstrument(t *testing.T) {
	t.Parallel()

	svc, _, md := newBackupRestoreTestService(t, false)
	archive, opts := backupRestoreTestArchive("")
	if _, err := svc.RestoreBackup(context.Background(), archive, opts); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	assertBackupRestoreManualPush(t, md, "")
}

func TestRestoreBackupMissingCommittedManualInstrumentFails(t *testing.T) {
	t.Parallel()

	svc, _, md := newBackupRestoreTestService(t, true)
	archive, opts := backupRestoreTestArchive("125.5")
	_, err := svc.RestoreBackup(context.Background(), archive, opts)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RestoreBackup error = %v, want not found", err)
	}
	if !strings.Contains(
		err.Error(),
		"backup restored; live manual market-data update pending reconciliation",
	) {
		t.Fatalf("RestoreBackup error = %q, want reconciliation warning", err)
	}
	if len(md.pushed) != 0 {
		t.Fatalf("manual pushes = %d, want 0", len(md.pushed))
	}
}

func TestRestoreBackupTopologyChangeRestartsWithoutManualPush(t *testing.T) {
	t.Parallel()

	svc, _, md := newBackupRestoreTestService(t, false)
	archive, opts := backupRestoreTestArchive("125.5")
	archive.Data.MarketDataInstruments[0].BaseAsset = "MSFT"
	if _, err := svc.RestoreBackup(context.Background(), archive, opts); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("stop=%d restart=%d, want 1 each", md.stops, md.restarts)
	}
	if len(md.pushed) != 0 {
		t.Fatalf("manual pushes = %d, want 0", len(md.pushed))
	}
}

func TestRestoreBackupReloadFailureStillRestartsMarketData(t *testing.T) {
	t.Parallel()

	svc, _, md := newBackupRestoreTestService(t, false)
	reloadErr := errors.New("test signer reload failure")
	svc.signer = &backupRestoreTestSigner{reloadErr: reloadErr}
	archive, opts := backupRestoreTestArchive("125.5")
	archive.Data.MarketDataInstruments[0].BaseAsset = "MSFT"

	_, err := svc.RestoreBackup(context.Background(), archive, opts)
	if !errors.Is(err, reloadErr) {
		t.Fatalf("RestoreBackup error = %v, want signer reload failure", err)
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("stop=%d restart=%d, want 1 each", md.stops, md.restarts)
	}
}

func assertBackupRestoreManualPush(
	t *testing.T,
	md *backupRestoreTestRuntime,
	wantPrice string,
) {
	t.Helper()
	if md.stops != 0 || md.restarts != 0 {
		t.Fatalf("stop=%d restart=%d, want 0 each", md.stops, md.restarts)
	}
	if len(md.pushed) != 1 {
		t.Fatalf("manual pushes = %d, want 1", len(md.pushed))
	}
	got := md.pushed[0]
	if got.BaseAssetID != backupRestoreBaseAssetID ||
		got.QuoteAssetID != backupRestoreQuoteAssetID {
		t.Fatalf(
			"pushed asset ids = (%d, %d), want (%d, %d)",
			got.BaseAssetID, got.QuoteAssetID,
			backupRestoreBaseAssetID, backupRestoreQuoteAssetID,
		)
	}
	if got.ManualPrice != wantPrice {
		t.Fatalf("pushed manual price = %q, want %q", got.ManualPrice, wantPrice)
	}
}

// backupRestoreSourceArchive builds an archive carrying only a manifest
// source, with no data rows and no restore options scope. The restore options
// used alongside it in these tests never set Scope, so the market-data
// restore plan is never engaged regardless of the source value under test.
func backupRestoreSourceArchive(source string) backup.Archive {
	return backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		source,
		backup.RealmLabel{Code: "test"},
		backup.Scope{All: true},
		backup.Data{},
		backup.CredentialFormPlaintext,
	)
}

// TestRestoreBackupRejectsUnmeaningfulManifestSource covers Source values that
// carry no meaningful content: the Go zero value (what an archive missing the
// JSON member or carrying it as "" both decode to - the two collapse to the
// same value once past JSON decoding, unlike at the HTTP layer) and a
// whitespace-only value, which would otherwise satisfy a naive Source == ""
// check.
func TestRestoreBackupRejectsUnmeaningfulManifestSource(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		source string
	}{
		{name: "absent/empty", source: ""},
		{name: "whitespace only", source: "   \t  "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, n, _ := newBackupRestoreTestService(t, false)
			archive := backupRestoreSourceArchive(tt.source)
			_, err := svc.RestoreBackup(context.Background(), archive,
				backup.RestoreOptions{Mode: backup.RestoreModeOverwrite})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup error = %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), "backup manifest source is required") {
				t.Fatalf("RestoreBackup error = %q, want it to name the missing source",
					err)
			}
			if n.restoreCalls != 0 {
				t.Fatalf("restore reached the node with an unmeaningful source")
			}
		})
	}
}

// TestRestoreBackupPassesManifestSourceThroughUnchanged asserts the emptiness
// check trims only for the test: a manifest source padded with whitespace
// must reach the store exactly as it arrived, not normalized.
func TestRestoreBackupPassesManifestSourceThroughUnchanged(t *testing.T) {
	t.Parallel()

	const source = "  raw test source  "
	svc, n, _ := newBackupRestoreTestService(t, false)
	archive := backupRestoreSourceArchive(source)
	if _, err := svc.RestoreBackup(context.Background(), archive,
		backup.RestoreOptions{Mode: backup.RestoreModeOverwrite}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if n.restoreCalls != 1 {
		t.Fatalf("restore calls = %d, want 1", n.restoreCalls)
	}
	if n.restoredArchive.Manifest.Source != source {
		t.Fatalf("restored manifest source = %q, want %q (verbatim)",
			n.restoredArchive.Manifest.Source, source)
	}
}
