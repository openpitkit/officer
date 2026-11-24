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

package officer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/access"
	"go.openpit.dev/officer/internal/accountmode"
)

// InitialSetup describes explicit first-run decisions for unattended local
// setup. AccountMode is single or multi; FundsLimitMode is enforce,
// review_with_hold, review_without_hold or track_only;
// Encryption is enabled or disabled and must match the startup master key.
// Signing must be disabled and Access open. Supplying these decisions confirms
// open access and, when selected, unencrypted storage.
type InitialSetup struct {
	AccountMode    string                    `json:"accountMode"`
	FundsLimitMode domain.SpotFundsLimitMode `json:"fundsLimitMode"`
	Encryption     string                    `json:"encryption"`
	Signing        string                    `json:"signing"`
	Access         string                    `json:"access"`
}

// LoadInitialSetup reads and validates first-run decisions at the input boundary.
// No file requested means no initialization. Unknown fields and extra JSON
// values are refused. Register checks the process master key and setup state.
func LoadInitialSetup(path string) (*InitialSetup, error) {
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("initial setup: open JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var setup *InitialSetup
	if err := decoder.Decode(&setup); err != nil {
		return nil, fmt.Errorf("initial setup: decode JSON: %w", err)
	}
	if setup == nil {
		return nil, fmt.Errorf("initial setup: expected a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("initial setup: expected exactly one JSON object")
	}
	if err := setup.validateChoices(); err != nil {
		return nil, err
	}
	return setup, nil
}

// Validate checks every decision before any database is created or changed.
// sealed reports whether the process has a master key.
func (s *InitialSetup) Validate(sealed bool) error {
	if err := s.validateChoices(); err != nil {
		return err
	}
	if (s.Encryption == "enabled") != sealed {
		return fmt.Errorf("initial setup: encryption must match the configured master key")
	}
	return nil
}

func (s *InitialSetup) validateChoices() error {
	if s.AccountMode != accountmode.ModeSingle && s.AccountMode != accountmode.ModeMulti {
		return fmt.Errorf("initial setup: accountMode must be single or multi")
	}
	if err := domain.ValidateSpotFundsLimitMode(s.FundsLimitMode); err != nil {
		return fmt.Errorf("initial setup: fundsLimitMode: %w", err)
	}
	if s.Access != "open" {
		return fmt.Errorf("initial setup: access must be open")
	}
	if s.Signing != "disabled" {
		return fmt.Errorf("initial setup: signing must be disabled")
	}
	if s.Encryption != "enabled" && s.Encryption != "disabled" {
		return fmt.Errorf("initial setup: encryption must be enabled or disabled")
	}
	return nil
}

func (s *InitialSetup) apply(ctx context.Context, cp backend.ControlPlane, scope *accountmode.Scope, service *access.Service) error {
	ctx = auth.ContextWithCaller(ctx, auth.SystemCaller())
	if _, err := accountmode.ApplyDuringSetup(ctx, cp, scope, service, accountmode.Request{Mode: s.AccountMode}); err != nil {
		return fmt.Errorf("initial setup: account mode: %w", err)
	}
	if _, err := cp.PutSpotFundsLimitMode(ctx, domain.LimitSpotFundsLimitMode{Scope: domain.ScopeGlobal, Mode: s.FundsLimitMode}); err != nil {
		return fmt.Errorf("initial setup: funds limit mode: %w", err)
	}
	if s.Encryption == "disabled" {
		if err := service.DeclineEncryption(ctx, true); err != nil {
			return err
		}
	}
	if _, err := service.CompleteSetup(ctx, access.ModeOpen, "", true); err != nil {
		return fmt.Errorf("initial setup: complete: %w", err)
	}
	return nil
}
