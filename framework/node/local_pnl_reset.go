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

package node

import (
	"context"
	"errors"
	"fmt"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// AccountPnlResetter is the optional local realized-account-P&L capability.
// runID is empty for a manual reset and system-owned for scheduled executions.
// Reusing a committed system runID returns current state without another reset.
type AccountPnlResetter interface {
	ResetAccountPnl(context.Context, domain.AccountID, string, domain.Caller) (domain.Account, error)
}

// PnlResetSchedulerOwner is the one explicitly installed local reset worker.
// Suspend stops and joins it before returning a resume operation; that operation
// must run after the node releases its restore/reset lane gates.
type PnlResetSchedulerOwner interface {
	Suspend() (func() error, error)
	// ValidateRestore runs under Suspend ownership, before any store write.
	ValidateRestore(context.Context, backup.Archive, backup.RestoreOptions) error
	Close() error
}

// PnlResetHost is the optional concrete-node composition capability.
type PnlResetHost interface {
	AccountPnlResetter
	InstallPnlResetScheduler(PnlResetSchedulerOwner) error
	PnlResetSchedulerFailure(error) error
}

// PnlResetSchedulerFailure fail-stops after a committed local schedule write
// loses its required audit or its worker cannot resume.
func (n *localNode) PnlResetSchedulerFailure(err error) error {
	return n.fatalReconciliation("scheduled account P&L reset", err)
}

// InstallPnlResetScheduler attaches one worker before the node starts serving.
func (n *localNode) InstallPnlResetScheduler(owner PnlResetSchedulerOwner) error {
	if owner == nil {
		return fmt.Errorf("missing P&L reset scheduler owner")
	}
	if n.pnlResetOwner != nil {
		return domain.ErrAlreadyExists
	}
	if _, ok := n.realm.(store.AccountPnlResetStore); !ok {
		return fmt.Errorf("reset scheduler requires atomic reset persistence")
	}
	if _, ok := n.realm.(store.BackupRollbackStore); !ok {
		return fmt.Errorf("reset scheduler requires scoped rollback capture")
	}
	n.pnlResetOwner = owner
	return nil
}

func (n *localNode) suspendPnlResetScheduler() (func() error, error) {
	if n.pnlResetOwner == nil {
		return func() error { return nil }, nil
	}
	resume, err := n.pnlResetOwner.Suspend()
	if err != nil {
		return nil, err
	}
	return func() error {
		if err := resume(); err != nil {
			return n.fatalReconciliation("resume reset scheduler after store replacement", err)
		}
		return nil
	}, nil
}

var errPnlResetRecorded = errors.New("account P&L reset already recorded")

// ResetAccountPnl force-assigns zero through the existing SDK account chain,
// including from halted/unset state. It preserves all holdings and blocks.
func (n *localNode) ResetAccountPnl(ctx context.Context, code domain.AccountID, runID string, caller domain.Caller) (domain.Account, error) {
	if caller.TightenOnly() {
		return domain.Account{}, fmt.Errorf("reset account P&L: %w", domain.ErrLoosening)
	}
	if err := domain.ValidateSource(caller.Source); err != nil {
		return domain.Account{}, domain.Internal(err)
	}
	if runID != "" && caller.Source != domain.SourceSystem {
		return domain.Account{}, domain.ErrForbidden
	}
	if err := domain.ValidateAccountID(code); err != nil {
		return domain.Account{}, err
	}
	journal, ok := n.realm.(store.AccountPnlResetStore)
	if !ok {
		return domain.Account{}, fmt.Errorf("account P&L reset requires atomic snapshot/audit persistence")
	}
	eng, endLane, err := n.beginLane()
	if err != nil {
		return domain.Account{}, err
	}
	defer endLane()
	identity, found, err := n.realm.GetAccount(ctx, code)
	if err != nil {
		return domain.Account{}, err
	}
	if !found {
		return domain.Account{}, domain.NewAccountMissingError(code)
	}
	var result domain.Account
	_, applied, err := n.runAccountRuntimeChain(ctx, eng, identity,
		"reset account realized pnl", nil, &accountRuntimePnl{
			value: "0", readCurrent: true,
			prepare: func(current domain.Account) error {
				result = current
				if runID != "" {
					done, err := journal.AccountPnlResetRecorded(ctx, current.EngineAccountID, runID)
					if err != nil {
						return err
					}
					if done {
						return errPnlResetRecorded
					}
				}
				return nil
			},
			onSet: func(previous domain.Account, blocks []domain.AccountBlock) error {
				durable := context.WithoutCancel(ctx)
				if err := n.mirrorPolicyConfigurationBlocks(durable, domain.PolicySpotFundsPnlBoundsKillSwitch, blocks); err != nil {
					return err
				}
				detailCode := domain.AuditDetailResetAccountPnl
				params := domain.AuditDetailParams{"account": code.String(), "previousPnl": previous.Pnl}
				if previous.PnlHaltReason != "" {
					detailCode = domain.AuditDetailResetAccountPnlPreviousHalt
					params["previousHalt"] = string(previous.PnlHaltReason)
				}
				entry := store.AuditEntry{
					Actor: caller.Principal, Source: caller.Source,
					Action: domain.AuditActionResetAccountPnl, Account: code,
					AccountTitle: previous.Title, AccountIdentity: previous.EngineAccountID,
					Detail: domain.NewAuditDetail(detailCode, params),
				}
				if err := journal.RecordAccountPnlReset(durable, previous, runID, entry); err != nil {
					return err
				}
				current, found, err := n.realm.GetAccount(durable, code)
				if err != nil {
					return err
				}
				if !found {
					return domain.ErrNotFound
				}
				result = current
				return nil
			},
		}, nil)
	if errors.Is(err, errPnlResetRecorded) {
		return result, nil
	}
	if err != nil && applied {
		var fatal internalPostCommitNodeMutationFailure
		if !errors.As(err, &fatal) {
			err = n.fatalPostEnginePersistence("reset account realized pnl", code, err)
		}
	}
	return result, err
}
