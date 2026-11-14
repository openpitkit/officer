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

	"go.openpit.dev/officer/framework/domain"
)

// TradingAccessObserverHost is an optional production composition capability.
// The observer runs synchronously after administrative completion, outside all
// mutation/runtime locks. Transition identities are captured under serialization.
// Observer failures report committed state, and never roll back access.
type TradingAccessObserverHost interface {
	InstallTradingAccessObserver(func(context.Context, domain.Account, domain.Caller) error) error
}

func (n *localNode) InstallTradingAccessObserver(observer func(context.Context, domain.Account, domain.Caller) error) error {
	if observer == nil {
		return fmt.Errorf("missing trading access observer: %w", domain.ErrInvalid)
	}
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()
	if n.tradingAccessObserver != nil {
		return fmt.Errorf("trading access observer already installed: %w", domain.ErrInvalid)
	}
	n.tradingAccessObserver = observer
	return nil
}

type tradingAccessState struct {
	account domain.Account
	mixed   bool
}

type tradingAccessCompletionKey struct{}

type tradingAccessCompletion struct {
	callbacks []func() error
}

// TradingAccessNotifications defers synchronous warning delivery to the outer
// administrative operation. Call finish after releasing every lock and completing
// runtime reconciliation, and join its error with the operation's result.
// Nested node calls reuse the outer scope; direct node callers own their scope.
func TradingAccessNotifications(ctx context.Context) (context.Context, func() error) {
	if _, ok := ctx.Value(tradingAccessCompletionKey{}).(*tradingAccessCompletion); ok {
		return ctx, func() error { return nil }
	}
	completion := &tradingAccessCompletion{}
	return context.WithValue(ctx, tradingAccessCompletionKey{}, completion), func() error {
		var failures []error
		for _, callback := range completion.callbacks {
			failures = append(failures, callback())
		}
		completion.callbacks = nil
		return errors.Join(failures...)
	}
}

// captureTradingAccessState is called only with mutate held. Empty selectors
// inspect the realm for restore; grants inspect only their effective subjects.
func (n *localNode) captureTradingAccessState(ctx context.Context, account domain.AccountID, group string) (map[domain.AccountID]tradingAccessState, error) {
	if n.tradingAccessObserver == nil {
		return nil, nil
	}
	accounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("read trading access accounts: %w", err)
	}
	connections, err := n.realm.ListTradingConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("read trading access connections: %w", err)
	}
	modes := make(map[domain.ExternalID]domain.TradingMode, len(connections))
	for _, connection := range connections {
		modes[connection.ExternalID] = connection.Mode
	}
	states := make(map[domain.AccountID]tradingAccessState)
	for _, row := range accounts {
		if account != "" && row.Code != account || group != "" && row.GroupCode != group {
			continue
		}
		access, err := n.realm.ListTradingAccess(ctx, row.Code)
		if err != nil {
			return nil, fmt.Errorf("read trading access for account %s: %w", row.Code, err)
		}
		var paper, live bool
		for _, grant := range access {
			mode, found := modes[grant.Connection]
			if !found {
				return nil, fmt.Errorf("trading connection %s for account %s: %w", grant.Connection, row.Code, domain.ErrNotFound)
			}
			switch mode {
			case domain.TradingModePaper:
				paper = true
			case domain.TradingModeLive:
				live = true
			default:
				return nil, fmt.Errorf("trading connection mode %q for account %s: %w", mode, row.Code, domain.ErrInvalid)
			}
		}
		states[row.Code] = tradingAccessState{account: row, mixed: paper && live}
	}
	return states, nil
}

func (n *localNode) publishTradingAccessTransitions(ctx context.Context, before map[domain.AccountID]tradingAccessState, account domain.AccountID, group string, caller domain.Caller) error {
	if n.tradingAccessObserver == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	completion, ok := ctx.Value(tradingAccessCompletionKey{}).(*tradingAccessCompletion)
	if !ok {
		return fmt.Errorf("missing trading access completion scope: %w", domain.ErrInvalid)
	}
	after, err := n.captureTradingAccessState(ctx, account, group)
	if err != nil {
		completion.callbacks = append(completion.callbacks, func() error {
			return &domain.TradingAccessNotificationError{Cause: fmt.Errorf("inspect committed mixed trading access: %w", err)}
		})
		return nil
	}
	observer := n.tradingAccessObserver
	// Reentrant operations and retained channel contexts must own a new scope,
	// rather than append to this draining or already completed collector.
	observerCtx := context.WithValue(ctx, tradingAccessCompletionKey{}, nil)
	for code, state := range after {
		if state.mixed && !before[code].mixed {
			completion.callbacks = append(completion.callbacks, func() error {
				if err := observer(observerCtx, state.account, caller); err != nil {
					return &domain.TradingAccessNotificationError{Cause: fmt.Errorf("publish mixed trading access warning for account %s: %w", code, err)}
				}
				return nil
			})
		}
	}
	return nil
}
