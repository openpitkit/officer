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

package access

import (
	"context"
	"fmt"
	"strings"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// SetupDecisions reads the choices owned by the account and credential components.
type SetupDecisions interface {
	// AccountModeChosen reports a stored, non-null account-mode choice.
	AccountModeChosen(ctx context.Context) (bool, error)
	// CredentialsSealed reports whether the master key is in effect.
	CredentialsSealed() bool
}

func (s *Service) requireSetup() error {
	switch mode := s.Mode(); mode {
	case ModeSetup:
		return nil
	case ModeOpen, ModeToken:
		return domain.ErrSetupComplete
	default:
		return fmt.Errorf("access: unknown access mode %q", mode)
	}
}

// SetupState reads the wizard's bookmark and first encryption decline time.
// After completion it returns ErrSetupComplete before consulting the store.
func (s *Service) SetupState(ctx context.Context) (SetupState, error) {
	if err := s.requireSetup(); err != nil {
		return SetupState{}, err
	}
	return s.store.SetupState(ctx)
}

// SetSetupStep saves a bookmark without checking step order or earlier choices.
func (s *Service) SetSetupStep(ctx context.Context, step SetupStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireSetup(); err != nil {
		return err
	}
	if _, err := ParseSetupStep(string(step)); err != nil {
		return domain.NewValidationError("/step", "enum", err.Error())
	}
	return s.store.SetSetupStep(ctx, step)
}

// DeclineEncryption records an acknowledged decline with the service clock.
// Repeating it keeps the first time; a sealed database cannot decline its key.
func (s *Service) DeclineEncryption(ctx context.Context, acknowledged bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireSetup(); err != nil {
		return err
	}
	if !acknowledged {
		return domain.NewValidationError(
			"/acknowledged", "required", "running without encryption must be acknowledged",
		)
	}
	if s.decisions.CredentialsSealed() {
		return fmt.Errorf("the master key is already in effect: %w", domain.ErrConflict)
	}
	return s.store.DeclineEncryption(ctx, s.now())
}

// CompleteSetup enters open or token mode once account mode and encryption
// have been decided. Token mode returns the new browser session id.
func (s *Service) CompleteSetup(
	ctx context.Context, mode Mode, password string, acknowledged bool,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireSetup(); err != nil {
		return "", err
	}
	caller, err := callerFrom(ctx)
	if err != nil {
		return "", err
	}
	switch mode {
	case ModeOpen:
		if !acknowledged {
			return "", domain.NewValidationError(
				"/acknowledged", "required", "open access must be acknowledged",
			)
		}
	case ModeToken:
		if err := validatePassword(password); err != nil {
			return "", err
		}
	default:
		return "", domain.NewValidationError("/access", "enum", "access must be open or token")
	}
	chosen, err := s.decisions.AccountModeChosen(ctx)
	if err != nil {
		return "", fmt.Errorf("access: read account mode choice: %w", err)
	}
	state, err := s.store.SetupState(ctx)
	if err != nil {
		return "", err
	}
	var missing []string
	if !chosen {
		missing = append(missing, "account mode")
	}
	if !s.decisions.CredentialsSealed() && state.EncryptionDeclinedAt == nil {
		missing = append(missing, "encryption")
	}
	if len(missing) != 0 {
		return "", fmt.Errorf("setup decisions missing: %s: %w", strings.Join(missing, ", "), domain.ErrConflict)
	}
	if mode == ModeToken {
		return s.enableTokenMode(ctx, caller, password, func(
			ctx context.Context, credentials Credentials, session Session,
		) error {
			return s.store.CompleteSetupToken(ctx, credentials, session, credentials.Password.SetAt)
		})
	}
	if err := s.store.CompleteSetupOpen(ctx, s.now()); err != nil {
		return "", fmt.Errorf("access: complete setup in open mode: %w", err)
	}
	return "", s.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetAccessMode,
		Detail: domain.NewAuditDetail(domain.AuditDetailSetAccessModeOpen, nil),
	})
}
