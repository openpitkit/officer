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

// RestoreBackup imports a portable archive and reconnects market-data feeds to
// the restored engine sink when runtime state changed.
func (s *Service) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	if opts.Mode == "" {
		return backup.RestoreSummary{},
			fmt.Errorf("backup restore mode: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return backup.RestoreSummary{}, err
	}

	runtimeRestore := backup.TouchesRuntime(opts.Scope)
	mdStopped := false
	if runtimeRestore && s.md != nil {
		s.md.Stop()
		mdStopped = true
	}
	summary, sink, err := n.RestoreBackup(
		ctx, archive, opts, auth.CallerFromContext(ctx),
	)
	if err != nil {
		if mdStopped {
			err = errors.Join(err, s.restoreMarketDataAfterBackup(sink))
		}
		return backup.RestoreSummary{},
			fmt.Errorf("backend: restore backup: %w", err)
	}
	if mdStopped {
		if err := s.restoreMarketDataAfterBackup(sink); err != nil {
			return backup.RestoreSummary{}, err
		}
	}
	return summary, nil
}

// ResetDatabase recreates the store from scratch and reconnects market-data
// feeds to the reset engine sink.
func (s *Service) ResetDatabase(ctx context.Context) error {
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
