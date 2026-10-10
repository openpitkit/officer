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
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// requireNoVenueOrder must run in the order's account lane.
func (n *localNode) requireNoVenueOrder(ctx context.Context, order domain.ExternalID) error {
	_, linked, err := n.realm.GetVenueOrder(ctx, order)
	if err != nil {
		return fmt.Errorf("get venue order: %w", err)
	}
	if linked {
		return fmt.Errorf("order %s: %w", order, domain.ErrVenueOrderManaged)
	}
	return nil
}

func (n *localNode) CredentialsSealed() bool {
	return n.realm.CredentialsSealed()
}

func (n *localNode) CreateTradingConnection(
	ctx context.Context, connection domain.TradingConnection, caller domain.Caller,
) (domain.TradingConnection, error) {
	if err := n.beginMutation(); err != nil {
		return domain.TradingConnection{}, err
	}
	defer n.endMutation()

	created, err := n.realm.CreateTradingConnection(ctx, connection)
	if err != nil {
		return domain.TradingConnection{}, fmt.Errorf("create trading connection: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetTrading,
		Detail: fmt.Sprintf("create trading connection %s (%s, %s)", created.ExternalID, created.Provider, created.Mode),
	}); err != nil {
		return domain.TradingConnection{}, fmt.Errorf("audit create trading connection: %w", err)
	}
	return created, nil
}

func (n *localNode) UpdateTradingConnectionSettings(
	ctx context.Context, id domain.ExternalID, label, credentials string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.UpdateTradingConnectionSettings(ctx, id, label, credentials); err != nil {
		return fmt.Errorf("update trading connection settings: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetTrading,
		Detail: fmt.Sprintf("update trading connection settings %s", id),
	}); err != nil {
		return fmt.Errorf("audit update trading connection settings: %w", err)
	}
	return nil
}

func (n *localNode) SetTradingConnectionEnabled(
	ctx context.Context, id domain.ExternalID, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetTradingConnectionEnabled(ctx, id, enabled); err != nil {
		return fmt.Errorf("set trading connection enabled: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetTrading,
		Detail: tradingConnectionToggleDetail(id, enabled),
	}); err != nil {
		return fmt.Errorf("audit set trading connection enabled: %w", err)
	}
	return nil
}

func (n *localNode) UpsertTradingInstrument(
	ctx context.Context, instrument domain.TradingInstrument, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.UpsertTradingInstrument(ctx, instrument); err != nil {
		return fmt.Errorf("upsert trading instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetTrading,
		Detail: fmt.Sprintf("upsert trading instrument %s/%s", instrument.Connection, instrument.ExternalSymbol),
	}); err != nil {
		return fmt.Errorf("audit upsert trading instrument: %w", err)
	}
	return nil
}

func (n *localNode) DeleteTradingInstrument(
	ctx context.Context, connection domain.ExternalID, externalSymbol string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteTradingInstrument(ctx, connection, externalSymbol); err != nil {
		return fmt.Errorf("delete trading instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetTrading,
		Detail: fmt.Sprintf("delete trading instrument %s/%s", connection, externalSymbol),
	}); err != nil {
		return fmt.Errorf("audit delete trading instrument: %w", err)
	}
	return nil
}

func (n *localNode) PutTradingAccess(
	ctx context.Context, access domain.TradingAccess, requireFeeAccount bool,
	caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	// The fee rule below reads the subject, so an invalid one is refused first.
	if err := domain.ValidateTradingAccess(access); err != nil {
		return fmt.Errorf("put trading access: %w", err)
	}
	if requireFeeAccount {
		connection, found, err := n.realm.GetTradingConnection(ctx, access.Connection)
		if err != nil {
			return fmt.Errorf("read trading connection: %w", err)
		}
		if !found {
			return fmt.Errorf("trading connection %s: %w", access.Connection, domain.ErrNotFound)
		}
		if connection.FeeAccount == "" {
			// A group can hold several senders at any time, so without a fee
			// account its grant is refused even as the first one.
			if access.AccountGroup != "" {
				return domain.ErrFeeAccountRequired
			}
			grants, err := n.realm.ListTradingAccessForConnection(ctx, access.Connection)
			if err != nil {
				return fmt.Errorf("read trading access: %w", err)
			}
			alreadyGranted := false
			for _, existing := range grants {
				if existing.Access.SameSubject(access) {
					alreadyGranted = true
					break
				}
			}
			if !alreadyGranted && len(grants) > 0 {
				return domain.ErrFeeAccountRequired
			}
		}
	}
	if err := n.realm.PutTradingAccess(ctx, access); err != nil {
		return fmt.Errorf("put trading access: %w", err)
	}
	if err := n.audit(ctx, caller, tradingAccessAudit("put", access)); err != nil {
		return fmt.Errorf("audit put trading access: %w", err)
	}
	return nil
}

func (n *localNode) SetTradingConnectionFeeAccount(
	ctx context.Context, connection domain.ExternalID, account domain.AccountID,
	caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	auditAccount := account
	detail := fmt.Sprintf("set trading connection fee account %s", connection)
	if account == "" {
		previous, accounts, err := n.realm.GetTradingFeeAccounts(ctx, connection)
		if err != nil {
			return fmt.Errorf("read trading connection fee accounts: %w", err)
		}
		auditAccount = previous
		detail = fmt.Sprintf("clear trading connection fee account %s", connection)
		if len(accounts) > 1 {
			return domain.ErrFeeAccountRequired
		}
		grants, err := n.realm.ListTradingAccessForConnection(ctx, connection)
		if err != nil {
			return fmt.Errorf("read trading access: %w", err)
		}
		for _, grant := range grants {
			if grant.Access.AccountGroup != "" {
				return domain.ErrFeeAccountRequired
			}
		}
	}
	if err := n.realm.SetTradingConnectionFeeAccount(ctx, connection, account); err != nil {
		return fmt.Errorf("set trading connection fee account: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetTrading,
		Account: auditAccount,
		Detail:  detail,
	}); err != nil {
		return fmt.Errorf("audit set trading connection fee account: %w", err)
	}
	return nil
}

func (n *localNode) RecordVenueFeeSkipped(
	ctx context.Context, connection domain.ExternalID, feeID, reason string,
	caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionVenueFeeSkipped,
		Detail: fmt.Sprintf("venue fee skipped connection %s fee %s: %s", connection, feeID, reason),
	}); err != nil {
		return fmt.Errorf("audit venue fee skipped: %w", err)
	}
	return nil
}

func (n *localNode) RemoveTradingAccess(
	ctx context.Context, connection domain.ExternalID,
	account domain.AccountID, accountGroup string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.RemoveTradingAccess(ctx, connection, account, accountGroup); err != nil {
		return fmt.Errorf("remove trading access: %w", err)
	}
	if err := n.audit(ctx, caller, tradingAccessAudit("remove", domain.TradingAccess{
		Account: account, AccountGroup: accountGroup, Connection: connection,
	})); err != nil {
		return fmt.Errorf("audit remove trading access: %w", err)
	}
	return nil
}

// tradingAccessAudit describes a grant change. The entry's Account or Group is
// the structured subject; the detail names it by its subject label, so a group
// grant does not read as an account grant.
func tradingAccessAudit(verb string, access domain.TradingAccess) store.AuditEntry {
	detail := fmt.Sprintf(
		"%s trading access %s/%s", verb, access.Connection, access.SubjectLabel(),
	)
	if access.VenueAccount != "" {
		detail += "/" + access.VenueAccount
	}
	return store.AuditEntry{
		Action:  domain.AuditActionSetTrading,
		Account: access.Account,
		Group:   access.AccountGroup,
		Detail:  detail,
	}
}

func (n *localNode) GetTradingConnection(
	ctx context.Context, id domain.ExternalID,
) (domain.TradingConnection, bool, error) {
	value, ok, err := n.realm.GetTradingConnection(ctx, id)
	if err != nil {
		return domain.TradingConnection{}, false, fmt.Errorf("get trading connection: %w", err)
	}
	return value, ok, nil
}

func (n *localNode) ListTradingConnections(
	ctx context.Context,
) ([]domain.TradingConnection, error) {
	value, err := n.realm.ListTradingConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("list trading connections: %w", err)
	}
	return value, nil
}

func (n *localNode) ListTradingInstruments(
	ctx context.Context, connection domain.ExternalID,
) ([]domain.TradingInstrument, error) {
	value, err := n.realm.ListTradingInstruments(ctx, connection)
	if err != nil {
		return nil, fmt.Errorf("list trading instruments: %w", err)
	}
	return value, nil
}

func (n *localNode) ListTradingAccess(
	ctx context.Context, account domain.AccountID,
) ([]domain.TradingAccess, error) {
	value, err := n.realm.ListTradingAccess(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("list trading access: %w", err)
	}
	return value, nil
}

func (n *localNode) ListTradingAccessForConnection(
	ctx context.Context, connection domain.ExternalID,
) ([]store.TradingAccessListRow, error) {
	value, err := n.realm.ListTradingAccessForConnection(ctx, connection)
	if err != nil {
		return nil, fmt.Errorf("list trading access for connection: %w", err)
	}
	return value, nil
}

func (n *localNode) GetVenueOrder(
	ctx context.Context, order domain.ExternalID,
) (domain.VenueOrder, bool, error) {
	value, ok, err := n.realm.GetVenueOrder(ctx, order)
	if err != nil {
		return domain.VenueOrder{}, false, fmt.Errorf("get venue order: %w", err)
	}
	return value, ok, nil
}

func (n *localNode) EarliestVenueOrderTime(
	ctx context.Context, connection domain.ExternalID,
) (time.Time, bool, error) {
	value, ok, err := n.realm.EarliestVenueOrderTime(ctx, connection)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("earliest venue order time: %w", err)
	}
	return value, ok, nil
}

func tradingConnectionToggleDetail(id domain.ExternalID, enabled bool) string {
	state := "disable"
	if enabled {
		state = "enable"
	}
	return fmt.Sprintf("%s trading connection %s", state, id)
}
