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

package node

import (
	"context"
	"errors"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// ensureAutoCreatedAccount creates a missing account and publishes its stable
// numeric id before any account lane can admit work for the alias.
func (n *localNode) ensureAutoCreatedAccount(
	ctx context.Context, id domain.AccountID, operation string, caller domain.Caller,
) error {
	if _, ok, err := n.realm.GetAccount(ctx, id); err != nil {
		return fmt.Errorf("read account for %s: %w", operation, err)
	} else if ok {
		return nil
	}
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	resolver := n.currentEngine()
	account, err := n.realm.CreateAccount(ctx, domain.Account{Code: id})
	if err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("create account for %s: %w", operation, err)
	}
	if err := resolver.AddAccountResolverEntry(account); err != nil {
		return n.rollbackAutoCreatedAccountPublication(
			ctx,
			account,
			fmt.Errorf("publish auto-created account for %s: %w", operation, err),
		)
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      account.Code,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("auto-created account %s by %s", account.Code, operation),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit auto-created account", "account", account.Code.String(),
			fmt.Errorf("audit create account: %w", err),
		)
	}
	return nil
}

func (n *localNode) rollbackAutoCreatedAccountPublication(
	ctx context.Context, account domain.Account, cause error,
) error {
	durableCtx := context.WithoutCancel(ctx)
	if err := n.realm.DeleteAccount(durableCtx, account.Code, false); err == nil {
		// The auto-create store write already committed before publication failed,
		// so this is a post-commit failure even though the rollback restored a
		// consistent state: ErrAlreadyExists from the resolver describes an internal
		// store/engine desync, not something the caller did wrong.
		return internalPostCommitNodeMutationError(cause)
	} else {
		reconcileErr := n.rebuildEngineFromStore(durableCtx)
		combined := errors.Join(
			cause,
			fmt.Errorf("rollback auto-created account %q: %w", account.Code, err),
			reconcileErr,
		)
		return n.fatalPostEngineAuditByCode(
			"rollback auto-created account publication",
			"account",
			account.Code.String(),
			combined,
		)
	}
}

// ensureAccount applies the request's missing-account choice. The caller must
// already hold a gate that excludes account lanes.
func (n *localNode) ensureAccount(
	ctx context.Context, id domain.AccountID,
	missing domain.MissingAccountPolicy, operation string, caller domain.Caller,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	switch missing {
	case domain.MissingAccountCreate:
		return n.ensureAutoCreatedAccount(ctx, id, operation, caller)
	case domain.MissingAccountReject:
		if _, ok, err := n.realm.GetAccount(ctx, id); err != nil {
			return fmt.Errorf("read account for %s: %w", operation, err)
		} else if !ok {
			return domain.NewAccountMissingError(id)
		}
		return nil
	default:
		return domain.ValidateMissingAccountPolicy(missing)
	}
}

func (n *localNode) ensureAccountAndAssetsRegistered(
	ctx context.Context, id domain.AccountID,
	missing domain.MissingAccountPolicy, operation string,
	caller domain.Caller, assets ...string,
) error {
	if err := n.ensureAccount(ctx, id, missing, operation, caller); err != nil {
		return err
	}
	for _, code := range assets {
		if code == "" {
			continue
		}
		if _, err := n.ensureAutoCreatedAsset(ctx, code, operation, caller); err != nil {
			return err
		}
	}
	return nil
}

// ensureAccountAndAssetsRegisteredExclusive resolves missing dictionary state
// under the live identity gate before the caller enters an account lane.
func (n *localNode) ensureAccountAndAssetsRegisteredExclusive(
	ctx context.Context, id domain.AccountID,
	missing domain.MissingAccountPolicy, operation string,
	caller domain.Caller, assets ...string,
) error {
	if err := domain.ValidateMissingAccountPolicy(missing); err != nil {
		return err
	}
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	_, accountExists, err := n.realm.GetAccount(ctx, id)
	if err != nil {
		return fmt.Errorf("read account for auto-create check: %w", err)
	}
	if !accountExists && missing == domain.MissingAccountReject {
		return domain.NewAccountMissingError(id)
	}
	needed := !accountExists
	for _, code := range assets {
		if code == "" {
			continue
		}
		if _, ok, err := n.realm.GetAsset(ctx, code); err != nil {
			return fmt.Errorf("read asset for auto-create check: %w", err)
		} else if !ok {
			needed = true
		}
	}
	if !needed {
		return nil
	}
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()
	return n.ensureAccountAndAssetsRegistered(
		ctx, id, missing, operation, caller, assets...,
	)
}
