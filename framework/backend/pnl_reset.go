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
	"fmt"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/scheduler"
)

// AccountPnlResetScheduleState explicitly distinguishes an absent definition
// from a configured job. RuntimeError reports a failed worker, even if a job
// still has ready state in storage.
type AccountPnlResetScheduleState struct {
	Job          *scheduler.Job `json:"job"`
	RuntimeError string         `json:"runtimeError"`
}

// AccountPnlResetScheduleInput edits one daily realm-wide definition. Revision
// is zero only when creating the first definition. Termination is explicit.
type AccountPnlResetScheduleInput struct {
	Enabled     bool                  `json:"enabled"`
	Time        string                `json:"time"`
	Timezone    string                `json:"timezone"`
	Termination scheduler.Termination `json:"termination"`
	Revision    int64                 `json:"revision"`
}

// AccountPnlResetSchedules is the explicitly installed local schedule owner.
type AccountPnlResetSchedules interface {
	Read(context.Context) (AccountPnlResetScheduleState, error)
	Save(context.Context, AccountPnlResetScheduleInput, domain.Caller) (AccountPnlResetScheduleState, error)
	Recover(context.Context, int64, string, domain.Caller) (AccountPnlResetScheduleState, error)
}

// AccountPnlResetControl is an optional surface capability, separate from
// ControlPlane. A composition registers its routes/tools only when installed.
type AccountPnlResetControl interface {
	ResetAccountPnl(context.Context, domain.AccountID) (domain.Account, error)
	AccountPnlResetSchedule(context.Context) (AccountPnlResetScheduleState, error)
	PutAccountPnlResetSchedule(context.Context, AccountPnlResetScheduleInput) (AccountPnlResetScheduleState, error)
	RecoverAccountPnlResetSchedule(context.Context, int64, string) (AccountPnlResetScheduleState, error)
}

type accountPnlResetControl struct {
	service       *Service
	reset         node.AccountPnlResetter
	schedules     AccountPnlResetSchedules
	scheduleScope AccountScope
}

// NewAccountPnlResetControl binds explicitly supplied local capabilities to
// the service's manual account scope, explicit schedule authority and caller attribution.
func NewAccountPnlResetControl(service *Service, reset node.AccountPnlResetter, schedules AccountPnlResetSchedules, scheduleScope AccountScope) (AccountPnlResetControl, error) {
	if service == nil || reset == nil || schedules == nil || scheduleScope == nil {
		return nil, fmt.Errorf("P&L reset requires service, account resetter, schedule owner and schedule authority")
	}
	return &accountPnlResetControl{service: service, reset: reset, schedules: schedules, scheduleScope: scheduleScope}, nil
}

func resetOperator(ctx context.Context) (domain.Caller, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.Caller{}, err
	}
	if err := domain.ValidateSource(caller.Source); err != nil {
		return domain.Caller{}, err
	}
	if caller.TightenOnly() {
		return domain.Caller{}, fmt.Errorf("reset P&L or change its schedule: %w", domain.ErrLoosening)
	}
	return caller, nil
}

func (s *accountPnlResetControl) ResetAccountPnl(ctx context.Context, requested domain.AccountID) (domain.Account, error) {
	caller, err := resetOperator(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	account, err := s.service.scope.Account(caller, requested)
	if err != nil {
		return domain.Account{}, err
	}
	if err := s.service.scope.Visible(caller, account); err != nil {
		return domain.Account{}, err
	}
	return s.reset.ResetAccountPnl(ctx, account, "", caller)
}

func (s *accountPnlResetControl) global(ctx context.Context, write bool) (domain.Caller, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.Caller{}, err
	}
	if write {
		caller, err = resetOperator(ctx)
		if err != nil {
			return domain.Caller{}, err
		}
		if err := s.scheduleScope.ManageAccounts(caller); err != nil {
			return domain.Caller{}, err
		}
	}
	restriction, err := s.scheduleScope.List(caller, "")
	if err != nil {
		return domain.Caller{}, err
	}
	if restriction.Restricted() {
		return domain.Caller{}, fmt.Errorf("daily P&L reset requires realm-wide account authority: %w", domain.ErrForbidden)
	}
	return caller, nil
}

func (s *accountPnlResetControl) AccountPnlResetSchedule(ctx context.Context) (AccountPnlResetScheduleState, error) {
	if _, err := s.global(ctx, false); err != nil {
		return AccountPnlResetScheduleState{}, err
	}
	return s.schedules.Read(ctx)
}

func (s *accountPnlResetControl) PutAccountPnlResetSchedule(ctx context.Context, input AccountPnlResetScheduleInput) (AccountPnlResetScheduleState, error) {
	caller, err := s.global(ctx, true)
	if err != nil {
		return AccountPnlResetScheduleState{}, err
	}
	return s.schedules.Save(ctx, input, caller)
}

func (s *accountPnlResetControl) RecoverAccountPnlResetSchedule(ctx context.Context, revision int64, action string) (AccountPnlResetScheduleState, error) {
	caller, err := s.global(ctx, true)
	if err != nil {
		return AccountPnlResetScheduleState{}, err
	}
	return s.schedules.Recover(ctx, revision, action, caller)
}
