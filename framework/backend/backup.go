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

package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

// ExportBackup returns a portable JSON-ready archive and conventional filename.
func (s *Service) ExportBackup(
	ctx context.Context,
	scope backup.Scope,
) (backup.Archive, string, error) {
	n, err := s.groupNode()
	if err != nil {
		return backup.Archive{}, "", err
	}
	archive, err := n.ExportBackup(ctx, scope, auth.CallerFromContext(ctx))
	if err != nil {
		return backup.Archive{}, "", fmt.Errorf("backend: export backup: %w", err)
	}
	return archive, backup.Filename(archive.Manifest.CreatedAt), nil
}

// RestoreBackup imports a portable archive. Market-data connectors keep running
// across online publication and residual engine rebuilds; they are stopped only
// when the effective restore changes their applied configuration.
func (s *Service) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()

	if opts.Mode == "" {
		return backup.RestoreSummary{},
			fmt.Errorf("backup restore mode: %w", domain.ErrInvalid)
	}
	// Trimming decides only whether the field is meaningful; the stored value
	// keeps the archive's Source exactly as it arrived.
	if strings.TrimSpace(archive.Manifest.Source) == "" {
		return backup.RestoreSummary{},
			fmt.Errorf("backup manifest source is required: %w", domain.ErrInvalid)
	}
	if err := backup.ValidateCredentialForm(archive.CredentialForm); err != nil {
		return backup.RestoreSummary{}, fmt.Errorf(
			"backup credential form %q unsupported: %w",
			archive.CredentialForm, err,
		)
	}
	data := backup.FilterData(archive.Data, opts.Scope.Normalize())
	marketDataInstances := make(
		[]domain.MarketDataInstance, 0, len(data.MarketDataInstances),
	)
	for _, archived := range data.MarketDataInstances {
		credentials := ""
		if archive.CredentialForm == backup.CredentialFormPlaintext {
			credentials = string(archived.Credentials)
		}
		marketDataInstances = append(marketDataInstances, domain.MarketDataInstance{
			ExternalID:  archived.ExternalID,
			Provider:    archived.Provider,
			Label:       archived.Label,
			Credentials: credentials,
			Enabled:     archived.Enabled,
		})
	}
	n, err := s.groupNode()
	if err != nil {
		return backup.RestoreSummary{}, err
	}

	mdPlan := marketDataRestorePlan{}
	if s.md != nil {
		mdPlan, err = planMarketDataRestore(
			ctx, n, archive, opts, marketDataInstances,
		)
		if err != nil {
			return backup.RestoreSummary{}, err
		}
	}
	mdStopped := false
	if mdPlan.restart && s.md != nil {
		s.md.Stop()
		mdStopped = true
	}
	restoreOpts := opts
	restoreOpts.ValidateMarketDataInstance = func(instance domain.MarketDataInstance) error {
		return validateMarketDataProvider(s.registry, instance)
	}
	summary, sink, err := n.RestoreBackup(
		ctx, archive, restoreOpts, auth.CallerFromContext(ctx),
	)
	var reloadErr error
	if reloadErr = s.signer.Reload(context.WithoutCancel(ctx)); reloadErr != nil {
		reloadErr = fmt.Errorf("backend: reload signer after backup restore: %w", reloadErr)
	}
	if err != nil {
		if mdStopped {
			err = errors.Join(err, s.restoreMarketDataAfterBackup(sink))
		}
		err = errors.Join(err, reloadErr)
		return backup.RestoreSummary{},
			fmt.Errorf("backend: restore backup: %w", err)
	}
	var postRestoreErr error
	if mdStopped {
		if err := s.restoreMarketDataAfterBackup(sink); err != nil {
			postRestoreErr = fmt.Errorf(
				"backend: backup restored; market-data restart pending reconciliation: %w",
				err,
			)
		}
	} else if s.md != nil {
		manualUpdates, err := listRestoredManualUpdates(
			ctx, n, mdPlan.manualUpdates,
		)
		if err != nil {
			postRestoreErr = fmt.Errorf(
				"backend: backup restored; live manual market-data update pending reconciliation: %w",
				err,
			)
		} else {
			for _, instrument := range manualUpdates {
				if err := s.md.PushManual(
					ctx, instrument.Instance.String(), instrument,
				); err != nil {
					postRestoreErr = fmt.Errorf(
						"backend: backup restored; live manual market-data update pending reconciliation: %w",
						err,
					)
					break
				}
			}
		}
	}
	if postRestoreErr != nil {
		return summary, errors.Join(postRestoreErr, reloadErr)
	}
	if reloadErr != nil {
		return summary, reloadErr
	}
	return summary, nil
}

type marketDataRestorePlan struct {
	restart       bool
	manualUpdates []domain.MarketDataInstrument
}

type marketDataInstanceRuntime struct {
	provider    string
	credentials string
	enabled     bool
}

type marketDataInstrumentRuntime struct {
	instance       domain.ExternalID
	externalSymbol string
	baseAsset      string
	quoteAsset     string
	enabled        bool
}

func planMarketDataRestore(
	ctx context.Context,
	n interface {
		ListMarketDataInstances(context.Context) ([]domain.MarketDataInstance, error)
		ListMarketDataInstruments(
			context.Context, domain.ExternalID,
		) ([]domain.MarketDataInstrument, error)
	},
	archive backup.Archive,
	opts backup.RestoreOptions,
	restoredInstances []domain.MarketDataInstance,
) (marketDataRestorePlan, error) {
	scope := opts.Scope.Normalize()
	if !scope.Included(backup.SectionMarketData) ||
		!archiveCarriesSection(archive, backup.SectionMarketData) {
		return marketDataRestorePlan{}, nil
	}
	// This layer does not hold the master key, so it cannot predict whether a
	// sealed credential will open. Restart against committed store state after
	// restore instead of pushing the archive representation into reconciliation.
	if archive.CredentialForm == backup.CredentialFormSealed {
		return marketDataRestorePlan{restart: true}, nil
	}

	currentInstances, err := n.ListMarketDataInstances(ctx)
	if err != nil {
		return marketDataRestorePlan{}, fmt.Errorf(
			"backend: list market-data instances before restore: %w", err,
		)
	}
	currentInstruments := make([]domain.MarketDataInstrument, 0)
	for _, instance := range currentInstances {
		instruments, err := n.ListMarketDataInstruments(ctx, instance.ExternalID)
		if err != nil {
			return marketDataRestorePlan{}, fmt.Errorf(
				"backend: list market-data instruments for %s before restore: %w",
				instance.ExternalID, err,
			)
		}
		currentInstruments = append(currentInstruments, instruments...)
	}

	data := backup.FilterData(archive.Data, scope)
	desiredInstances, desiredInstruments := projectMarketDataRestore(
		currentInstances, currentInstruments,
		restoredInstances, data.MarketDataInstruments,
		opts.Mode,
	)
	if !sameMarketDataRuntime(
		currentInstances, currentInstruments,
		desiredInstances, desiredInstruments,
	) {
		return marketDataRestorePlan{restart: true}, nil
	}

	currentByKey := make(map[string]domain.MarketDataInstrument, len(currentInstruments))
	for _, instrument := range currentInstruments {
		currentByKey[marketDataInstrumentKey(instrument)] = instrument
	}
	desiredInstancesByID := make(
		map[domain.ExternalID]domain.MarketDataInstance, len(desiredInstances),
	)
	for _, instance := range desiredInstances {
		desiredInstancesByID[instance.ExternalID] = instance
	}
	manualUpdates := make([]domain.MarketDataInstrument, 0)
	for _, instrument := range desiredInstruments {
		previous, ok := currentByKey[marketDataInstrumentKey(instrument)]
		instance := desiredInstancesByID[instrument.Instance]
		if !ok || previous.ManualPrice == instrument.ManualPrice ||
			instance.Provider != domain.MarketDataProviderBYO ||
			!instance.Enabled || !instrument.Enabled {
			continue
		}
		manualUpdates = append(manualUpdates, instrument)
	}
	sort.Slice(manualUpdates, func(i, j int) bool {
		left, right := manualUpdates[i], manualUpdates[j]
		if left.Instance != right.Instance {
			return left.Instance.String() < right.Instance.String()
		}
		return left.ExternalSymbol < right.ExternalSymbol
	})
	return marketDataRestorePlan{manualUpdates: manualUpdates}, nil
}

func archiveCarriesSection(archive backup.Archive, section backup.Section) bool {
	for _, carried := range archive.Manifest.Sections {
		if carried == section {
			return true
		}
	}
	return false
}

func projectMarketDataRestore(
	currentInstances []domain.MarketDataInstance,
	currentInstruments []domain.MarketDataInstrument,
	archivedInstances []domain.MarketDataInstance,
	archivedInstruments []backup.MarketDataInstrument,
	mode backup.RestoreMode,
) ([]domain.MarketDataInstance, []domain.MarketDataInstrument) {
	instances := make(map[domain.ExternalID]domain.MarketDataInstance)
	instruments := make(map[string]domain.MarketDataInstrument)
	if mode != backup.RestoreModeReplaceAll {
		for _, instance := range currentInstances {
			instances[instance.ExternalID] = instance
		}
		for _, instrument := range currentInstruments {
			instruments[marketDataInstrumentKey(instrument)] = instrument
		}
	}
	for _, instance := range archivedInstances {
		if _, exists := instances[instance.ExternalID]; mode == backup.RestoreModeInsertMissing && exists {
			continue
		}
		instances[instance.ExternalID] = instance
	}
	for _, archived := range archivedInstruments {
		instrument := domain.MarketDataInstrument{
			Instance:       archived.Instance,
			ExternalSymbol: archived.ExternalSymbol,
			BaseAsset:      archived.BaseAsset,
			QuoteAsset:     archived.QuoteAsset,
			ManualPrice:    archived.ManualPrice,
			Enabled:        archived.Enabled,
		}
		key := marketDataInstrumentKey(instrument)
		if _, exists := instruments[key]; mode == backup.RestoreModeInsertMissing && exists {
			continue
		}
		instruments[key] = instrument
	}
	instanceRows := make([]domain.MarketDataInstance, 0, len(instances))
	for _, instance := range instances {
		instanceRows = append(instanceRows, instance)
	}
	instrumentRows := make([]domain.MarketDataInstrument, 0, len(instruments))
	for _, instrument := range instruments {
		instrumentRows = append(instrumentRows, instrument)
	}
	return instanceRows, instrumentRows
}

func sameMarketDataRuntime(
	leftInstances []domain.MarketDataInstance,
	leftInstruments []domain.MarketDataInstrument,
	rightInstances []domain.MarketDataInstance,
	rightInstruments []domain.MarketDataInstrument,
) bool {
	leftInstanceRuntime := make(
		map[domain.ExternalID]marketDataInstanceRuntime, len(leftInstances),
	)
	for _, instance := range leftInstances {
		leftInstanceRuntime[instance.ExternalID] = marketDataInstanceRuntime{
			provider: instance.Provider, credentials: instance.Credentials,
			enabled: instance.Enabled,
		}
	}
	rightInstanceRuntime := make(
		map[domain.ExternalID]marketDataInstanceRuntime, len(rightInstances),
	)
	for _, instance := range rightInstances {
		rightInstanceRuntime[instance.ExternalID] = marketDataInstanceRuntime{
			provider: instance.Provider, credentials: instance.Credentials,
			enabled: instance.Enabled,
		}
	}
	if !equalMarketDataInstanceRuntime(leftInstanceRuntime, rightInstanceRuntime) {
		return false
	}
	leftInstrumentRuntime := make(map[string]marketDataInstrumentRuntime, len(leftInstruments))
	for _, instrument := range leftInstruments {
		leftInstrumentRuntime[marketDataInstrumentKey(instrument)] = marketDataInstrumentRuntime{
			instance: instrument.Instance, externalSymbol: instrument.ExternalSymbol,
			baseAsset: instrument.BaseAsset, quoteAsset: instrument.QuoteAsset,
			enabled: instrument.Enabled,
		}
	}
	rightInstrumentRuntime := make(map[string]marketDataInstrumentRuntime, len(rightInstruments))
	for _, instrument := range rightInstruments {
		rightInstrumentRuntime[marketDataInstrumentKey(instrument)] = marketDataInstrumentRuntime{
			instance: instrument.Instance, externalSymbol: instrument.ExternalSymbol,
			baseAsset: instrument.BaseAsset, quoteAsset: instrument.QuoteAsset,
			enabled: instrument.Enabled,
		}
	}
	if len(leftInstrumentRuntime) != len(rightInstrumentRuntime) {
		return false
	}
	for key, left := range leftInstrumentRuntime {
		if rightInstrumentRuntime[key] != left {
			return false
		}
	}
	return true
}

func equalMarketDataInstanceRuntime(
	left, right map[domain.ExternalID]marketDataInstanceRuntime,
) bool {
	if len(left) != len(right) {
		return false
	}
	for id, runtime := range left {
		if right[id] != runtime {
			return false
		}
	}
	return true
}

func marketDataInstrumentKey(instrument domain.MarketDataInstrument) string {
	return instrument.Instance.String() + "\x00" + instrument.ExternalSymbol
}

func listRestoredManualUpdates(
	ctx context.Context,
	n interface {
		ListMarketDataInstruments(
			context.Context, domain.ExternalID,
		) ([]domain.MarketDataInstrument, error)
	},
	planned []domain.MarketDataInstrument,
) ([]domain.MarketDataInstrument, error) {
	storedByKey := make(map[string]domain.MarketDataInstrument, len(planned))
	listedInstances := make(map[domain.ExternalID]struct{})
	for _, update := range planned {
		if _, listed := listedInstances[update.Instance]; listed {
			continue
		}
		instruments, err := n.ListMarketDataInstruments(ctx, update.Instance)
		if err != nil {
			return nil, fmt.Errorf(
				"backend: list market-data instruments for %s after restore: %w",
				update.Instance, err,
			)
		}
		listedInstances[update.Instance] = struct{}{}
		for _, instrument := range instruments {
			storedByKey[marketDataInstrumentKey(instrument)] = instrument
		}
	}

	updates := make([]domain.MarketDataInstrument, 0, len(planned))
	for _, update := range planned {
		key := marketDataInstrumentKey(update)
		stored, ok := storedByKey[key]
		if !ok {
			return nil, fmt.Errorf(
				"backend: restored market-data instrument %q for instance %s not found: %w",
				update.ExternalSymbol, update.Instance, domain.ErrNotFound,
			)
		}
		updates = append(updates, stored)
	}
	return updates, nil
}

// ResetDatabase recreates the store from scratch and reconnects market-data
// feeds to the reset engine sink.
func (s *Service) ResetDatabase(ctx context.Context) error {
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()

	n, err := s.groupNode()
	if err != nil {
		return err
	}

	mdStopped := false
	if s.md != nil {
		s.md.Stop()
		mdStopped = true
	}
	sink, err := n.ResetDatabase(ctx, auth.CallerFromContext(ctx))
	if err != nil {
		if mdStopped {
			err = errors.Join(err, s.restoreMarketDataAfterBackup(sink))
		}
		return fmt.Errorf("backend: reset database: %w", err)
	}
	if mdStopped {
		if err := s.restoreMarketDataAfterBackup(sink); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) restoreMarketDataAfterBackup(sink marketdata.Sink) error {
	var out error
	if sink != nil {
		if err := s.md.UseSink(sink); err != nil {
			out = errors.Join(out,
				fmt.Errorf("backend: restore market-data sink: %w", err))
		}
	}
	if err := s.md.Restart(); err != nil {
		out = errors.Join(out,
			fmt.Errorf("backend: restart market-data after restore: %w", err))
	}
	return out
}
