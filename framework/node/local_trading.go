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

// SetTradingConnectionEnabled refuses a tighten-only caller's enable before
// anything changes, whether the connection is disabled or not: an enable
// widens what can be sent, and the backend restarts the trading runtime after
// it. A disable passes.
func (n *localNode) SetTradingConnectionEnabled(
	ctx context.Context, id domain.ExternalID, enabled bool, caller domain.Caller,
) error {
	if enabled && caller.TightenOnly() {
		return fmt.Errorf(
			"enable trading connection %s: an enable widens what can be sent: %w",
			id, domain.ErrLoosening,
		)
	}
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

// UpsertTradingInstrument refuses a tighten-only caller before anything
// changes, whatever the upsert would do - add, enable, remap or disable a
// mapping: a mapping decides what can be sent and which Officer asset a venue
// fee is booked in, and a fee whose venue asset maps to no asset or to several
// is skipped (see the trading runtime's fee booking).
func (n *localNode) UpsertTradingInstrument(
	ctx context.Context, instrument domain.TradingInstrument, caller domain.Caller,
) error {
	if caller.TightenOnly() {
		return fmt.Errorf(
			"upsert trading instrument %s/%s: a mapping decides what can be sent "+
				"and which asset a venue fee is booked in: %w",
			instrument.Connection, instrument.ExternalSymbol, domain.ErrLoosening,
		)
	}
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

// DeleteTradingInstrument refuses a tighten-only caller before anything
// changes, for the reason UpsertTradingInstrument gives: a removed mapping can
// leave a venue fee's asset unresolved, and the fee is then skipped.
func (n *localNode) DeleteTradingInstrument(
	ctx context.Context, connection domain.ExternalID, externalSymbol string, caller domain.Caller,
) error {
	if caller.TightenOnly() {
		return fmt.Errorf(
			"delete trading instrument %s/%s: a mapping decides what can be sent "+
				"and which asset a venue fee is booked in: %w",
			connection, externalSymbol, domain.ErrLoosening,
		)
	}
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

// PutTradingAccess refuses a tighten-only caller's grant before anything is
// read or changed: a grant widens what its account or group may send to.
func (n *localNode) PutTradingAccess(
	ctx context.Context, access domain.TradingAccess, requireFeeAccount bool,
	caller domain.Caller,
) error {
	if caller.TightenOnly() {
		return fmt.Errorf(
			"put trading access %s: a grant widens what can be sent: %w",
			access.Connection, domain.ErrLoosening,
		)
	}
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
			// The group check runs before the grants are read.
			if err := domain.ValidateTradingAccessWithoutFeeAccount(access, nil); err != nil {
				return err
			}
			rows, err := n.realm.ListTradingAccessForConnection(ctx, access.Connection)
			if err != nil {
				return fmt.Errorf("read trading access: %w", err)
			}
			grants := make([]domain.TradingAccess, 0, len(rows))
			for _, row := range rows {
				grants = append(grants, row.Access)
			}
			if err := domain.ValidateTradingAccessWithoutFeeAccount(access, grants); err != nil {
				return err
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

// SetTradingConnectionFeeAccount refuses a tighten-only caller's set and clear
// alike before anything is read or changed: either moves where future venue
// fees are debited, and a set fee account also lets the connection take a
// group grant or a grant to a second account.
func (n *localNode) SetTradingConnectionFeeAccount(
	ctx context.Context, connection domain.ExternalID, account domain.AccountID,
	caller domain.Caller,
) error {
	if caller.TightenOnly() {
		return fmt.Errorf(
			"set trading connection fee account %s: a set or clear moves where "+
				"future venue fees are debited: %w",
			connection, domain.ErrLoosening,
		)
	}
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

// RemoveTradingAccess refuses a tighten-only caller, when requireFeeAccount is
// true and the connection has no fee account, the revoke after which no grant
// of the connection reaches an account while before it they reached exactly
// one: the trading runtime debits such a connection's venue fees to the one
// account its grants reach and skips them when they reach none or several, so
// only that revoke turns a debit into a skip. The fee account and the grants
// are read under the mutation lock the write takes, so the check holds for the
// write. Every other revoke passes for every caller.
func (n *localNode) RemoveTradingAccess(
	ctx context.Context, connection domain.ExternalID,
	account domain.AccountID, accountGroup string, requireFeeAccount bool,
	caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if requireFeeAccount && caller.TightenOnly() {
		feeAccount, senders, err := n.realm.GetTradingFeeAccounts(ctx, connection)
		if err != nil {
			return fmt.Errorf("read trading connection fee accounts: %w", err)
		}
		if feeAccount == "" && len(senders) == 1 {
			reached, err := n.otherGrantsReachAnAccount(ctx, connection, domain.TradingAccess{
				Account: account, AccountGroup: accountGroup,
			})
			if err != nil {
				return err
			}
			if !reached {
				return fmt.Errorf(
					"remove trading access %s: the connection has no fee account, its "+
						"grants reach exactly one account and no other grant reaches "+
						"it, so its venue fees would be skipped instead of debited: %w",
					connection, domain.ErrLoosening,
				)
			}
		}
	}
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

// otherGrantsReachAnAccount reports whether a grant of connection other than
// the one to revoked's subject reaches an account, by the rule the store's
// GetTradingFeeAccounts applies: an account grant reaches its account, a group
// grant its group's current members.
func (n *localNode) otherGrantsReachAnAccount(
	ctx context.Context, connection domain.ExternalID, revoked domain.TradingAccess,
) (bool, error) {
	rows, err := n.realm.ListTradingAccessForConnection(ctx, connection)
	if err != nil {
		return false, fmt.Errorf("read trading access: %w", err)
	}
	for _, row := range rows {
		if row.Access.SameSubject(revoked) {
			continue
		}
		if row.Access.Account != "" {
			return true, nil
		}
		members, err := n.realm.ListGroupAccounts(ctx, row.Access.AccountGroup)
		if err != nil {
			return false, fmt.Errorf("list group accounts: %w", err)
		}
		if len(members) > 0 {
			return true, nil
		}
	}
	return false, nil
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
