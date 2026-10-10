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

// Package accountmode is Pit Officer's account mode: whether control-plane
// clients see and choose accounts (multi-account mode, today's behaviour) or
// every client request acts on one default account (single-account mode), so
// a client may omit the account from request bodies, query parameters and MCP
// inputs; a REST route that carries the account in its path still takes the
// default account's code there. The mode is a realm setting. Load reads it
// once, when the process starts, and builds the account scope the composition
// installs for the life of the process; Apply stores a change, which takes
// effect when the process next starts.
package accountmode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

// The realm setting that holds the account mode, and the modes.
const (
	// SettingKeyMode holds the chosen mode: ModeMulti, or ModeSingle followed
	// by ":" and the code of the default account it acts on ("single:desk").
	// One value keeps the mode and its account in one write, so no reader and
	// no other writer ever sees one without the other. It is absent until a
	// mode is chosen, which behaves as ModeMulti.
	SettingKeyMode = "account_mode"

	// ModeSingle fixes every client request to the default account.
	ModeSingle = "single"
	// ModeMulti lets clients see and choose every account and group.
	ModeMulti = "multi"
)

// singlePrefix opens a stored ModeSingle choice; the default account's code
// follows it.
const singlePrefix = ModeSingle + ":"

// defaultAccount is the code of the account Apply creates when single-account
// mode is chosen while no account exists.
const defaultAccount domain.AccountID = "default"

// Load reads the stored account mode once, when the process starts, and
// returns the account scope the composition installs for the life of the
// process: backend.EveryAccount when no mode is chosen or the mode is
// ModeMulti, the single-account scope of the stored default account when it is
// ModeSingle. A stored choice no start accepts - an unknown mode, or
// single-account mode without an account - fails the start.
//
// In single-account mode a default account that no longer exists (a backup
// restore or a database edit removed it) is recreated through the node,
// audited as a system action. Nothing else is ever created implicitly.
func Load(ctx context.Context, n node.Node) (backend.AccountScope, error) {
	_, features, err := readStored(ctx, n)
	if err != nil {
		return nil, err
	}
	if features.Accounts() {
		return backend.EveryAccount{}, nil
	}
	if _, _, err := n.GetAccountState(ctx, features.Account); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf(
				"accountmode: read default account %q: %w", features.Account, err,
			)
		}
		// fallback(approved): the stored default account is recreated, as a
		// system action the node audits, so single-account mode keeps the
		// account it acts on.
		if _, err := n.CreateAccount(
			ctx, domain.Account{Code: features.Account}, auth.SystemCaller(),
		); err != nil {
			return nil, fmt.Errorf(
				"accountmode: recreate default account %q: %w", features.Account, err,
			)
		}
	}
	return singleAccount{account: features.Account}, nil
}

// settings reads realm settings: the node provides it at start, and the
// running control plane on a mode switch.
type settings interface {
	Setting(ctx context.Context, key string) (string, bool, error)
}

// readStored reads the stored mode choice into a State's Mode and, under
// ModeSingle, Account, and returns it with the account features a process
// started with that choice enforces: every account when no mode is chosen or
// the mode is ModeMulti, the stored default account when it is ModeSingle. A
// choice no start accepts is an error naming its setting.
func readStored(
	ctx context.Context, s settings,
) (State, backend.AccountFeatures, error) {
	value, chosen, err := s.Setting(ctx, SettingKeyMode)
	if err != nil {
		return State{}, backend.AccountFeatures{}, fmt.Errorf(
			"accountmode: read setting %s: %w", SettingKeyMode, err,
		)
	}
	if !chosen {
		return State{}, backend.AccountFeatures{}, nil
	}
	account, single := strings.CutPrefix(value, singlePrefix)
	switch {
	case value == ModeMulti:
		mode := ModeMulti
		return State{Mode: &mode}, backend.AccountFeatures{}, nil
	case single:
		// The cause is kept as text: a stored setting is not a client's
		// invalid input.
		if err := domain.ValidateAccountID(domain.AccountID(account)); err != nil {
			return State{}, backend.AccountFeatures{}, fmt.Errorf(
				"accountmode: setting %s does not hold the default account "+
					"single-account mode needs: %v", SettingKeyMode, err,
			)
		}
		mode := ModeSingle
		return State{Mode: &mode, Account: account},
			backend.AccountFeatures{Account: domain.AccountID(account)}, nil
	default:
		return State{}, backend.AccountFeatures{}, fmt.Errorf(
			"accountmode: setting %s holds unknown mode %q", SettingKeyMode, value,
		)
	}
}

// singleAccount is the account scope of single-account mode. A client request
// acts on the default account alone: an omitted account is that account and
// another one is refused, lists return only its rows and the rows of no
// account, a resource of another account is reported as absent, account
// groups are not accepted, and the account set cannot change. The system
// caller (venue processing) acts on any account exactly as under
// backend.EveryAccount.
type singleAccount struct {
	account domain.AccountID
}

var _ backend.AccountScope = singleAccount{}

// isSystem reports whether caller is the system caller, which the scope
// answers exactly as backend.EveryAccount does.
func isSystem(caller domain.Caller) bool {
	return caller.Source == domain.SourceSystem
}

// Features fixes the default account for every client.
func (s singleAccount) Features() backend.AccountFeatures {
	return backend.AccountFeatures{Account: s.account}
}

// Account resolves an omitted account and the default account itself to the
// default account, and refuses any other.
func (s singleAccount) Account(
	caller domain.Caller, requested domain.AccountID,
) (domain.AccountID, error) {
	if isSystem(caller) {
		return backend.EveryAccount{}.Account(caller, requested)
	}
	if err := s.accepts(requested); err != nil {
		return "", err
	}
	return s.account, nil
}

// MissingAccount keeps a policy the client sent, which the backend validates;
// an omitted one is reject, as the default account always exists.
func (s singleAccount) MissingAccount(
	caller domain.Caller, requested domain.MissingAccountPolicy,
) (domain.MissingAccountPolicy, error) {
	if isSystem(caller) {
		return backend.EveryAccount{}.MissingAccount(caller, requested)
	}
	if requested != "" {
		return requested, nil
	}
	return domain.MissingAccountReject, nil
}

// List restricts every client list to the default account and the rows of no
// account; a list narrowed to another account is refused.
func (s singleAccount) List(
	caller domain.Caller, requested domain.AccountID,
) (domain.AccountRestriction, error) {
	if isSystem(caller) {
		return backend.EveryAccount{}.List(caller, requested)
	}
	if err := s.accepts(requested); err != nil {
		return domain.AccountRestriction{}, err
	}
	return domain.AccountRestriction{Account: s.account, IncludeUnowned: true}, nil
}

// Visible shows a client the resources of the default account and of no
// account. Any other is reported as absent, without naming its account.
func (s singleAccount) Visible(caller domain.Caller, owner domain.AccountID) error {
	if isSystem(caller) {
		return backend.EveryAccount{}.Visible(caller, owner)
	}
	if owner == "" || owner == s.account {
		return nil
	}
	return domain.ErrNotFound
}

// Group refuses every account group a client names.
func (s singleAccount) Group(caller domain.Caller, requested string) error {
	if isSystem(caller) {
		return backend.EveryAccount{}.Group(caller, requested)
	}
	if requested == "" {
		return nil
	}
	return domain.NewValidationError(
		"/accountGroup", "single_account_mode",
		"account groups are not accepted in single-account mode",
	)
}

// ManageAccounts refuses every client change of the account set.
func (s singleAccount) ManageAccounts(caller domain.Caller) error {
	if isSystem(caller) {
		return backend.EveryAccount{}.ManageAccounts(caller)
	}
	return fmt.Errorf(
		"the account set cannot be changed in single-account mode: %w",
		domain.ErrAccountsNotManaged,
	)
}

// accepts refuses an account a client named other than the default account.
func (s singleAccount) accepts(requested domain.AccountID) error {
	if requested == "" || requested == s.account {
		return nil
	}
	return domain.NewValidationError(
		"/account", "single_account_mode", fmt.Sprintf(
			"single-account mode acts only on the default account %q: "+
				"another account is not accepted", s.account,
		),
	)
}

// Request is a mode change a client asks for.
type Request struct {
	// Mode is the mode to store: ModeSingle or ModeMulti.
	Mode string `json:"mode"`
	// Account names the default account for ModeSingle; empty leaves the
	// choice to Apply. ModeMulti takes none.
	Account string `json:"account"`
}

// State is the stored mode choice and what the running process enforces.
type State struct {
	// Mode is the stored mode; nil when none is chosen yet, which behaves as
	// ModeMulti.
	Mode *string
	// Account is the stored default account; empty unless the stored mode is
	// ModeSingle.
	Account string
	// Features is what this running process enforces: the running
	// composition's account features.
	Features backend.AccountFeatures
	// RestartRequired reports that the stored choice enforces other account
	// features than the running process does: it takes effect after a
	// restart.
	RestartRequired bool
}

// mu serialises Apply as a whole. Apply reads the account set, may create the
// default account, and only then stores the choice, so two concurrent requests
// for single-account mode could otherwise both find no account and both try to
// create it. A process-local lock is enough: Apply runs only in the serving
// process - no MCP tool switches the mode.
var mu sync.Mutex

// Read returns the stored mode choice and what the running process, served by
// cp, enforces.
func Read(ctx context.Context, cp backend.ControlPlane) (State, error) {
	state, stored, err := readStored(ctx, cp)
	if err != nil {
		return State{}, err
	}
	state.Features = cp.AccountFeatures()
	state.RestartRequired = stored != state.Features
	return state, nil
}

// Apply stores the mode choice req asks for through cp, the running
// composition's control plane, under the caller in ctx. The choice takes
// effect when the process next starts: the running account scope never
// changes. Apply returns the stored state; a refused request stores nothing.
//
// ModeMulti stores no default account. ModeSingle, in a process that
// runs multi-account mode, takes the named account, which must exist (else
// domain.AccountMissingError); with none named it takes the one existing
// account, or creates the account "default" when there is none, and refuses
// to choose among several. In a
// process that already runs single-account mode only its own default account
// is accepted: changing it needs multi-account mode and a restart first.
func Apply(ctx context.Context, cp backend.ControlPlane, req Request) (State, error) {
	mu.Lock()
	defer mu.Unlock()
	switch req.Mode {
	case "":
		return State{}, domain.NewValidationError(
			"/mode", "required", fmt.Sprintf(
				"account mode is required: use %q or %q", ModeSingle, ModeMulti,
			),
		)
	case ModeMulti:
		if req.Account != "" {
			return State{}, domain.NewValidationError(
				"/account", "unsupported_value",
				"multi-account mode takes no account",
			)
		}
		if err := cp.SetSetting(ctx, SettingKeyMode, ModeMulti); err != nil {
			return State{}, err
		}
	case ModeSingle:
		account, err := singleModeAccount(ctx, cp, domain.AccountID(req.Account))
		if err != nil {
			return State{}, err
		}
		if err := cp.SetSetting(ctx, SettingKeyMode, singlePrefix+string(account)); err != nil {
			return State{}, err
		}
	default:
		return State{}, domain.NewValidationError(
			"/mode", "enum", fmt.Sprintf(
				"account mode %q is not supported: use %q or %q",
				req.Mode, ModeSingle, ModeMulti,
			),
		)
	}
	return Read(ctx, cp)
}

// singleModeAccount resolves the default account a ModeSingle request stores
// (see Apply).
func singleModeAccount(
	ctx context.Context, cp backend.ControlPlane, requested domain.AccountID,
) (domain.AccountID, error) {
	if running := cp.AccountFeatures(); !running.Accounts() {
		if requested != "" && requested != running.Account {
			return "", fmt.Errorf(
				"single-account mode runs with its default account: to choose "+
					"another one, switch to multi-account mode and restart "+
					"first: %w", domain.ErrAccountsNotManaged,
			)
		}
		return running.Account, nil
	}
	if requested != "" {
		if _, _, err := cp.GetAccountState(ctx, requested); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return "", domain.NewAccountMissingError(requested)
			}
			return "", err
		}
		return requested, nil
	}
	accounts, err := cp.ListAccounts(ctx)
	if err != nil {
		return "", err
	}
	switch len(accounts) {
	case 0:
		// The client chose single-account mode, so creating its account is
		// the explicit, audited action of this request.
		created, err := cp.CreateAccount(ctx, domain.Account{Code: defaultAccount})
		if err != nil {
			return "", err
		}
		return created.Code, nil
	case 1:
		return accounts[0].Code, nil
	default:
		return "", domain.NewValidationError(
			"/account", "required", fmt.Sprintf(
				"%d accounts exist: name the one single-account mode acts on",
				len(accounts),
			),
		)
	}
}
