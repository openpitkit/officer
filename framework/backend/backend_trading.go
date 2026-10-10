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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

// TradingProvider exposes capabilities without the connector factory.
type TradingProvider struct {
	Type, Title                                     string
	VenueAccounts, VerifiesSymbols, SearchesSymbols bool
	UnlinkedFees                                    bool
}

// TradingConnectionStatus contains configuration without credential values.
type TradingConnectionStatus struct {
	ExternalID    domain.ExternalID
	Provider      string
	Label         string
	VenueIdentity string
	Mode          domain.TradingMode
	Enabled       bool
	// FeeAccount is the connection's fee account; empty when it has none or
	// when the account scope does not show it to the caller.
	FeeAccount domain.AccountID
	// State is running, unavailable or stopped, independently of Enabled.
	State       string
	Unavailable string
	Secrets     map[string]bool
	Instruments []domain.TradingInstrument
	// Access lists the connection's grants the account scope shows the caller,
	// each with the granted account's current group.
	Access []store.TradingAccessListRow
}

// TradingStatus is the credential-free control-plane view of trading.
type TradingStatus struct {
	Providers         []TradingProvider
	Connections       []TradingConnectionStatus
	CredentialsSealed bool
}

// TradingDestinationStatus joins an account's effective grant, its own or its
// group's, with the granted connection's state.
type TradingDestinationStatus struct {
	Connection   domain.ExternalID
	Label        string
	Provider     string
	Mode         domain.TradingMode
	Enabled      bool
	VenueAccount string
	State        string
	Unavailable  string
}

// SendDestination selects a connection and its connector-read route JSON.
type SendDestination struct {
	// Connection may be omitted when exactly one enabled binding has the mode.
	Connection domain.ExternalID
	// VenueAccount defaults to the binding; a different value is forbidden.
	VenueAccount string
	// Route is required JSON object text without surrounding whitespace.
	Route string
}

// VenueSendResult preserves the pre-trade approval even if sending is unknown.
type VenueSendResult struct {
	Token ApprovalToken
	// Send is nil when the pre-trade verdict is reject.
	Send *VenueSend
}

// VenueSend describes the outcome after an order has been held.
type VenueSend struct {
	// Status is acknowledged, refused, or unknown.
	Status string
	Error  string
	// Order is the stored venue link; nil when no link exists.
	Order *domain.VenueOrder
}

// DestinationAmbiguousError lists eligible destinations, as returned by
// ListTradingDestinations, so the caller can name a connection explicitly.
type DestinationAmbiguousError struct {
	Candidates []TradingDestinationStatus
}

func (e *DestinationAmbiguousError) Error() string {
	return domain.ErrDestinationAmbiguous.Error()
}

func (e *DestinationAmbiguousError) Unwrap() error {
	return domain.ErrDestinationAmbiguous
}

// TradingSymbolVerification distinguishes unsupported verification from absence.
type TradingSymbolVerification struct {
	Supported, Exists, Tradable bool
	Details                     string
}

// TradingSymbolSearch distinguishes unsupported search from an empty result.
type TradingSymbolSearch struct {
	Supported bool
	Matches   []trading.SymbolMatch
}

// GetTrading returns stored configuration and the current runtime states,
// narrowed to what the account scope shows the caller: a connection keeps the
// account grants whose account is visible and the group grants of the groups
// the caller sees, by visibleGroups, and reports its fee account only when that
// account is visible, otherwise exactly as a connection with no fee account.
func (s *Service) GetTrading(ctx context.Context) (TradingStatus, error) {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return TradingStatus{}, err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return TradingStatus{}, err
	}
	groupVisible, err := s.visibleGroups(ctx, caller)
	if err != nil {
		return TradingStatus{}, err
	}
	hidden := func(row store.TradingAccessListRow) bool {
		if row.Access.AccountGroup != "" {
			return !groupVisible(row.Access.AccountGroup)
		}
		return s.scope.Visible(caller, row.Access.Account) != nil
	}
	connections, err := s.node.ListTradingConnections(ctx)
	if err != nil {
		return TradingStatus{}, err
	}
	status := TradingStatus{
		Connections:       make([]TradingConnectionStatus, 0, len(connections)),
		CredentialsSealed: s.node.CredentialsSealed(),
	}
	for _, p := range s.trading.Providers() {
		status.Providers = append(status.Providers, TradingProvider{
			Type: p.Type, Title: p.Title, VenueAccounts: p.VenueAccounts,
			VerifiesSymbols: p.VerifiesSymbols, SearchesSymbols: p.SearchesSymbols,
			UnlinkedFees: p.UnlinkedFees,
		})
	}
	states := s.trading.ConnectionStates()
	for _, c := range connections {
		instruments, err := s.node.ListTradingInstruments(ctx, c.ExternalID)
		if err != nil {
			return TradingStatus{}, err
		}
		access, err := s.node.ListTradingAccessForConnection(ctx, c.ExternalID)
		if err != nil {
			return TradingStatus{}, err
		}
		access = slices.DeleteFunc(slices.Clone(access), hidden)
		feeAccount := c.FeeAccount
		if feeAccount != "" && s.scope.Visible(caller, feeAccount) != nil {
			feeAccount = ""
		}
		state, reason := tradingConnectionState(c.ExternalID, states)
		secrets, reason, err := tradingCredentialStatus(c.Credentials, reason)
		if err != nil {
			return TradingStatus{}, err
		}
		status.Connections = append(status.Connections, TradingConnectionStatus{
			ExternalID: c.ExternalID, Provider: c.Provider, Label: c.Label,
			VenueIdentity: c.VenueIdentity, Mode: c.Mode, Enabled: c.Enabled,
			FeeAccount: feeAccount, State: state, Unavailable: reason,
			Secrets: secrets, Instruments: instruments, Access: access,
		})
	}
	return status, nil
}

// CreateTradingConnection identifies the venue account before storing a paper
// connection. The returned value omits credentials.
func (s *Service) CreateTradingConnection(ctx context.Context, c domain.TradingConnection) (domain.TradingConnection, error) {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return domain.TradingConnection{}, err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.TradingConnection{}, err
	}
	c.Provider = strings.TrimSpace(c.Provider)
	c.Label = strings.TrimSpace(c.Label)
	c.Mode = domain.TradingMode(strings.TrimSpace(string(c.Mode)))
	c.Credentials = strings.TrimSpace(c.Credentials)
	c.FeeAccount = domain.AccountID(strings.TrimSpace(string(c.FeeAccount)))
	c.VenueIdentity = ""
	if c.Mode == domain.TradingModeLive {
		return domain.TradingConnection{}, fmt.Errorf("create trading connection: %w", domain.ErrLiveTradingUnavailable)
	}
	if !domain.TradingModeSupported(c.Mode) {
		return domain.TradingConnection{}, domain.ErrInvalid
	}
	if _, err := s.tradingProvider(c.Provider); err != nil {
		return domain.TradingConnection{}, err
	}
	if err := domain.ValidateTradingConnectionSettings(c.Label, c.Credentials); err != nil {
		return domain.TradingConnection{}, err
	}
	if c.FeeAccount != "" {
		return domain.TradingConnection{}, fmt.Errorf(
			"set the fee account after creating the connection: %w", domain.ErrInvalid)
	}
	info, err := s.trading.CheckAccount(ctx, c)
	if errors.Is(err, trading.ErrUnsupported) {
		return domain.TradingConnection{}, fmt.Errorf(
			"trading provider cannot identify its venue account: %w: %w", domain.ErrInvalid, trading.ErrUnsupported)
	}
	// A provider that refuses the request's credentials before reaching the
	// venue reports a validation error naming the member, not an upstream one.
	if pointer := domain.ValidationPointer(err); errors.Is(err, domain.ErrInvalid) &&
		(pointer == "/credentials" || strings.HasPrefix(pointer, "/credentials/")) {
		_, message, redactErr := tradingCredentialStatus(c.Credentials, err.Error())
		if redactErr != nil {
			return domain.TradingConnection{}, redactErr
		}
		return domain.TradingConnection{}, &redactedTradingError{text: message, cause: err}
	}
	if err != nil {
		return domain.TradingConnection{}, tradingUpstreamError("check account for create", c, err)
	}
	c.VenueIdentity = info.Identity
	if err := domain.ValidateTradingConnection(c); err != nil {
		return domain.TradingConnection{}, err
	}
	created, err := s.node.CreateTradingConnection(ctx, c, caller)
	if err != nil {
		return domain.TradingConnection{}, err
	}
	if created.Enabled {
		if err := s.restartTradingLocked(); err != nil {
			return domain.TradingConnection{}, err
		}
	}
	created.Credentials = ""
	return created, nil
}

// UpdateTradingConnectionSettings merges credentials without allowing a change
// of venue account. Empty labels and credential values keep stored settings.
func (s *Service) UpdateTradingConnectionSettings(ctx context.Context, id, label, credentials string) error {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	c, err := s.tradingConnection(ctx, id)
	if err != nil {
		return err
	}
	label = strings.TrimSpace(label)
	if label != "" {
		c.Label = label
	}
	credentials = strings.TrimSpace(credentials)
	if credentials != "" {
		if err := domain.ValidateTradingConnectionSettings(c.Label, credentials); err != nil {
			return err
		}
	}
	merged, err := mergeMarketDataCredentials(c.Credentials, credentials)
	if err != nil {
		return err
	}
	var storedValues, mergedValues map[string]any
	if err := json.Unmarshal([]byte(c.Credentials), &storedValues); err != nil {
		return fmt.Errorf("stored trading credentials are not a JSON object: %w", domain.ErrInvalid)
	}
	if err := json.Unmarshal([]byte(merged), &mergedValues); err != nil {
		return fmt.Errorf("merged trading credentials are not a JSON object: %w", domain.ErrInvalid)
	}
	changed := !reflect.DeepEqual(storedValues, mergedValues)
	c.Credentials = merged
	if err := domain.ValidateTradingConnection(c); err != nil {
		return err
	}
	if changed {
		info, err := s.trading.CheckAccount(ctx, c)
		if err != nil {
			return tradingUpstreamError("check account for update", c, err)
		}
		if info.Identity != c.VenueIdentity {
			return fmt.Errorf("the keys reach a different venue account; create a new connection: %w", domain.ErrConflict)
		}
	}
	if err := s.node.UpdateTradingConnectionSettings(ctx, c.ExternalID, c.Label, c.Credentials, caller); err != nil {
		return err
	}
	if changed {
		return s.restartTradingLocked()
	}
	return nil
}

// SetTradingConnectionEnabled starts enabled connections; disabling leaves
// runtime reconciliation of existing venue orders active.
func (s *Service) SetTradingConnectionEnabled(ctx context.Context, id string, enabled bool) error {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	connection, err := domain.ParseExternalID(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if err := s.node.SetTradingConnectionEnabled(ctx, connection, enabled, caller); err != nil {
		return err
	}
	if enabled {
		return s.restartTradingLocked()
	}
	return nil
}

// CheckTradingConnection checks the stored keys against their venue account.
func (s *Service) CheckTradingConnection(ctx context.Context, id string) (trading.AccountInfo, error) {
	if err := s.requireTradingRuntime(); err != nil {
		return trading.AccountInfo{}, err
	}
	c, err := s.tradingConnection(ctx, id)
	if err != nil {
		return trading.AccountInfo{}, err
	}
	info, err := s.trading.CheckAccount(ctx, c)
	if err != nil {
		return trading.AccountInfo{}, tradingUpstreamError("check account", c, err)
	}
	return info, nil
}

// UpsertTradingInstrument stores a venue-symbol mapping to local assets.
// Changes apply to subsequent orders without restarting the connection.
func (s *Service) UpsertTradingInstrument(ctx context.Context, inst domain.TradingInstrument) error {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	inst.ExternalSymbol = strings.TrimSpace(inst.ExternalSymbol)
	inst.BaseAsset = strings.TrimSpace(inst.BaseAsset)
	inst.QuoteAsset = strings.TrimSpace(inst.QuoteAsset)
	if err := domain.ValidateTradingInstrument(inst); err != nil {
		return err
	}
	return s.node.UpsertTradingInstrument(ctx, inst, caller)
}

// DeleteTradingInstrument removes a venue-symbol mapping from a connection.
// Stored venue links for existing orders remain intact.
func (s *Service) DeleteTradingInstrument(ctx context.Context, connection, externalSymbol string) error {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	id, err := domain.ParseExternalID(strings.TrimSpace(connection))
	if err != nil {
		return err
	}
	externalSymbol = strings.TrimSpace(externalSymbol)
	if externalSymbol == "" {
		return domain.ErrInvalid
	}
	return s.node.DeleteTradingInstrument(ctx, id, externalSymbol, caller)
}

// VerifyTradingSymbol asks the venue whether a symbol exists and is tradable.
// Unsupported providers return Supported=false without changing mappings.
func (s *Service) VerifyTradingSymbol(ctx context.Context, id, external string) (TradingSymbolVerification, error) {
	if err := s.requireTradingRuntime(); err != nil {
		return TradingSymbolVerification{}, err
	}
	external = strings.TrimSpace(external)
	if external == "" {
		return TradingSymbolVerification{}, domain.ErrInvalid
	}
	c, err := s.tradingConnection(ctx, id)
	if err != nil {
		return TradingSymbolVerification{}, err
	}
	result, err := s.trading.VerifySymbol(ctx, c, external)
	if errors.Is(err, trading.ErrUnsupported) {
		return TradingSymbolVerification{Supported: false}, nil
	}
	if err != nil {
		return TradingSymbolVerification{}, tradingUpstreamError("verify symbol", c, err)
	}
	return TradingSymbolVerification{Supported: true, Exists: result.Exists, Tradable: result.Tradable, Details: result.Details}, nil
}

// SearchTradingSymbols asks the venue for symbols matching a non-empty query.
// Unsupported providers return Supported=false without changing mappings.
func (s *Service) SearchTradingSymbols(ctx context.Context, id, query string) (TradingSymbolSearch, error) {
	if err := s.requireTradingRuntime(); err != nil {
		return TradingSymbolSearch{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return TradingSymbolSearch{}, domain.ErrInvalid
	}
	c, err := s.tradingConnection(ctx, id)
	if err != nil {
		return TradingSymbolSearch{}, err
	}
	matches, err := s.trading.SearchSymbols(ctx, c, query)
	if errors.Is(err, trading.ErrUnsupported) {
		return TradingSymbolSearch{Supported: false}, nil
	}
	if err != nil {
		return TradingSymbolSearch{}, tradingUpstreamError("search symbols", c, err)
	}
	return TradingSymbolSearch{Supported: true, Matches: matches}, nil
}

// PutTradingAccess grants an Officer account or an account group access to a
// connection and venue account. On a provider with unlinked fees, a connection
// without a fee account refuses a group grant, and an account grant while it
// holds any other grant, with domain.ErrFeeAccountRequired.
func (s *Service) PutTradingAccess(ctx context.Context, access domain.TradingAccess) error {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	access.Account, err = s.scopeTradingSubject(caller, access.Account, access.AccountGroup)
	if err != nil {
		return err
	}
	if err := domain.ValidateTradingAccess(access); err != nil {
		return err
	}
	c, err := s.tradingConnection(ctx, access.Connection.String())
	if err != nil {
		return err
	}
	p, err := s.tradingProvider(c.Provider)
	if err != nil {
		return err
	}
	return s.node.PutTradingAccess(ctx, access, p.UnlinkedFees, caller)
}

// RemoveTradingAccess revokes the grant of a trading connection to exactly one
// subject: account or accountGroup, the other empty. A malformed subject
// returns domain.ErrInvalid before any lookup. Existing venue-managed orders
// continue through their stored venue links.
func (s *Service) RemoveTradingAccess(ctx context.Context, connection domain.ExternalID, account domain.AccountID, accountGroup string) error {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	account, err = s.scopeTradingSubject(caller, account, accountGroup)
	if err != nil {
		return err
	}
	if err := domain.ValidateTradingAccess(domain.TradingAccess{
		Account: account, AccountGroup: accountGroup, Connection: connection,
	}); err != nil {
		return err
	}
	return s.node.RemoveTradingAccess(ctx, connection, account, accountGroup, caller)
}

// scopeTradingSubject passes the subject of a trading grant through the account
// scope: a named group is checked and the account is kept as sent, otherwise
// the account is resolved, so a request naming neither acts on the scope's
// account.
func (s *Service) scopeTradingSubject(
	caller domain.Caller, account domain.AccountID, group string,
) (domain.AccountID, error) {
	if group != "" {
		if err := s.scope.Group(caller, group); err != nil {
			return "", err
		}
		return account, nil
	}
	return s.scope.Account(caller, account)
}

// SetTradingFeeAccount accepts an empty account to clear the fee target.
// Providers without unlinked fees refuse both setting and clearing.
func (s *Service) SetTradingFeeAccount(ctx context.Context, connection domain.ExternalID, account domain.AccountID) error {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	// An empty account clears the fee account and names none to resolve.
	if account != "" {
		if account, err = s.scope.Account(caller, account); err != nil {
			return err
		}
	}
	c, err := s.tradingConnection(ctx, connection.String())
	if err != nil {
		return err
	}
	p, err := s.tradingProvider(c.Provider)
	if err != nil {
		return err
	}
	if !p.UnlinkedFees {
		return fmt.Errorf("trading provider has no unlinked fees: %w", domain.ErrInvalid)
	}
	return s.node.SetTradingConnectionFeeAccount(ctx, connection, account, caller)
}

// ListTradingDestinations includes disabled and unavailable connections among
// the account's effective grants, its own or its group's. Empty mode lists both
// paper and live connections, ordered by label.
func (s *Service) ListTradingDestinations(ctx context.Context, account domain.AccountID, mode domain.TradingMode) ([]TradingDestinationStatus, error) {
	s.tradingMu.Lock()
	defer s.tradingMu.Unlock()
	if err := s.requireTradingRuntime(); err != nil {
		return nil, err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if account, err = s.scope.Account(caller, account); err != nil {
		return nil, err
	}
	if mode != "" && !domain.TradingModeSupported(mode) {
		return nil, domain.ErrInvalid
	}
	if _, _, err := s.node.GetAccountState(ctx, account); err != nil {
		return nil, err
	}
	access, err := s.node.ListTradingAccess(ctx, account)
	if err != nil {
		return nil, err
	}
	states := s.trading.ConnectionStates()
	destinations := make([]TradingDestinationStatus, 0, len(access))
	for _, a := range access {
		c, err := s.tradingConnection(ctx, a.Connection.String())
		if err != nil {
			return nil, err
		}
		if mode != "" && c.Mode != mode {
			continue
		}
		state, reason := tradingConnectionState(c.ExternalID, states)
		_, reason, err = tradingCredentialStatus(c.Credentials, reason)
		if err != nil {
			return nil, err
		}
		destinations = append(destinations, TradingDestinationStatus{
			Connection: c.ExternalID, Label: c.Label, Provider: c.Provider,
			Mode: c.Mode, Enabled: c.Enabled, VenueAccount: a.VenueAccount,
			State: state, Unavailable: reason,
		})
	}
	sort.Slice(destinations, func(i, j int) bool { return destinations[i].Label < destinations[j].Label })
	return destinations, nil
}

func (s *Service) requireTradingRuntime() error {
	if s.trading == nil {
		return fmt.Errorf("trading runtime is not configured: %w", domain.ErrNotImplemented)
	}
	return nil
}

// SendPaperOrder pre-checks at the venue before taking a pre-trade hold. Once
// held, its approval and order id are returned even for an unknown send outcome.
func (s *Service) SendPaperOrder(ctx context.Context, o domain.Order, dest SendDestination) (VenueSendResult, error) {
	return s.sendOrder(ctx, o, dest, domain.TradingModePaper)
}

func (s *Service) sendOrder(ctx context.Context, o domain.Order, dest SendDestination, mode domain.TradingMode) (result VenueSendResult, err error) {
	var redact *strings.Replacer
	defer func() {
		if redact == nil {
			return
		}
		if err != nil {
			if text := redact.Replace(err.Error()); text != err.Error() {
				err = &redactedTradingError{text: text, cause: err}
			}
		}
		if result.Send != nil {
			result.Send.Error = redact.Replace(result.Send.Error)
		}
	}()
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return VenueSendResult{}, err
	}
	if o.Account, err = s.scope.Account(caller, o.Account); err != nil {
		return VenueSendResult{}, err
	}
	if err := domain.ValidateAccountID(o.Account); err != nil {
		return VenueSendResult{}, err
	}
	if err := domain.ValidateTradingRoute(dest.Route); err != nil {
		return VenueSendResult{}, err
	}
	if err := s.requireTradingRuntime(); err != nil {
		return VenueSendResult{}, err
	}
	var venueAccount string
	var connection domain.TradingConnection
	if !dest.Connection.IsZero() {
		connection, err = s.tradingConnection(ctx, dest.Connection.String())
		if err != nil {
			return VenueSendResult{}, err
		}
		_, redact, err = tradingCredentialRedactor(connection.Credentials)
		if err != nil {
			return VenueSendResult{}, err
		}
		access, err := s.node.ListTradingAccess(ctx, o.Account)
		if err != nil {
			return VenueSendResult{}, err
		}
		bound := false
		for _, binding := range access {
			if binding.Connection == connection.ExternalID {
				bound, venueAccount = true, binding.VenueAccount
				break
			}
		}
		if !bound {
			return VenueSendResult{}, fmt.Errorf("trading connection is not bound to the account: %w", domain.ErrForbidden)
		}
		if connection.Mode != mode {
			return VenueSendResult{}, fmt.Errorf("trading connection mode is %s, send requires %s: %w", connection.Mode, mode, domain.ErrTradingModeMismatch)
		}
		dest.Connection = connection.ExternalID
	} else {
		destinations, err := s.ListTradingDestinations(ctx, o.Account, mode)
		if err != nil {
			return VenueSendResult{}, err
		}
		candidates := destinations[:0]
		for _, destination := range destinations {
			if destination.Enabled {
				candidates = append(candidates, destination)
			}
		}
		switch len(candidates) {
		case 0:
			return VenueSendResult{}, fmt.Errorf("trading: %w", domain.ErrNoDestination)
		case 1:
			dest.Connection, venueAccount = candidates[0].Connection, candidates[0].VenueAccount
		default:
			return VenueSendResult{}, &DestinationAmbiguousError{Candidates: candidates}
		}
		connection, err = s.tradingConnection(ctx, dest.Connection.String())
		if err != nil {
			return VenueSendResult{}, err
		}
		_, redact, err = tradingCredentialRedactor(connection.Credentials)
		if err != nil {
			return VenueSendResult{}, err
		}
	}
	if dest.VenueAccount != "" && dest.VenueAccount != venueAccount {
		return VenueSendResult{}, fmt.Errorf("trading venue account differs from the binding: %w", domain.ErrForbidden)
	}
	destination := domain.TradingDestination{Connection: dest.Connection, VenueAccount: venueAccount, Route: dest.Route}
	if err := s.trading.PreCheck(ctx, o, destination); err != nil {
		if errors.Is(err, trading.ErrRejected) || errors.Is(err, trading.ErrUnsupported) {
			return VenueSendResult{}, fmt.Errorf("trading pre-check: %w: %w", err, domain.ErrInvalid)
		}
		if tradingHasDomainSentinel(err) {
			return VenueSendResult{}, err
		}
		return VenueSendResult{}, fmt.Errorf("trading pre-check: %w: %w", err, domain.ErrUpstream)
	}
	token, err := s.SubmitOrderToken(ctx, o, SubmitModeHold, domain.MissingAccountReject)
	if err != nil {
		return VenueSendResult{}, err
	}
	result = VenueSendResult{Token: token}
	if token.Verdict == "reject" {
		return result, nil
	}
	orderID := domain.ExternalID(token.OrderExternalID)
	_, sendErr := s.trading.Send(ctx, orderID, destination)
	result.Send = &VenueSend{Status: "acknowledged"}
	if sendErr != nil {
		result.Send.Status, result.Send.Error = "unknown", sendErr.Error()
		if errors.Is(sendErr, trading.ErrNotSent) {
			text := redact.Replace(sendErr.Error())
			reason := domain.NormalizeReason(redact.Replace(domain.NormalizeReason(text)))
			if _, _, cancelErr := s.CancelOrder(context.WithoutCancel(ctx), string(orderID), "0", reason); cancelErr != nil {
				result.Send.Error = errors.Join(sendErr, cancelErr).Error()
			} else {
				result.Send.Status, result.Send.Error = "refused", text
			}
			return result, nil
		} else if errors.Is(sendErr, trading.ErrRejected) || errors.Is(sendErr, trading.ErrUnsupported) {
			detail, readErr := s.node.GetOrder(context.WithoutCancel(ctx), orderID)
			if readErr == nil && detail.Order.Status == domain.OrderStatusCancelled {
				result.Send.Status = "refused"
			} else if readErr != nil {
				result.Send.Error = errors.Join(sendErr,
					errors.New("venue refused; cancellation could not be confirmed"),
					readErr).Error()
			} else {
				result.Send.Error = errors.Join(sendErr,
					errors.New("venue refused; cancellation is not recorded yet; runtime retries it")).Error()
			}
		}
	}
	// Send may stop waiting when ctx is cancelled while the worker still sends.
	link, found, err := s.node.GetVenueOrder(context.WithoutCancel(ctx), orderID)
	if err != nil {
		result.Send.Status = "unknown"
		if result.Send.Error != "" {
			result.Send.Error += "\n"
		}
		result.Send.Error += fmt.Errorf("trading: read venue order: %w", err).Error()
	} else if found {
		result.Send.Order = &link
	}
	return result, nil
}

func tradingHasDomainSentinel(err error) bool {
	if domain.IsSentinel(err) {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, cause := range wrapped.Unwrap() {
			if tradingHasDomainSentinel(cause) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return tradingHasDomainSentinel(wrapped.Unwrap())
	}
	return false
}

func (s *Service) tradingConnection(ctx context.Context, id string) (domain.TradingConnection, error) {
	connection, err := domain.ParseExternalID(strings.TrimSpace(id))
	if err != nil {
		return domain.TradingConnection{}, err
	}
	c, found, err := s.node.GetTradingConnection(ctx, connection)
	if err != nil {
		return domain.TradingConnection{}, err
	}
	if !found {
		return domain.TradingConnection{}, fmt.Errorf("trading connection %s: %w", connection, domain.ErrNotFound)
	}
	return c, nil
}

func (s *Service) tradingProvider(provider string) (trading.Provider, error) {
	for _, p := range s.trading.Providers() {
		if p.Type == provider {
			return p, nil
		}
	}
	return trading.Provider{}, fmt.Errorf("unknown trading provider: %w", domain.ErrInvalid)
}

func (s *Service) restartTradingLocked() error {
	if err := s.trading.Restart(); err != nil {
		return fmt.Errorf("backend: restart trading runtime after configuration was saved: %w", err)
	}
	return nil
}

func tradingUpstreamError(operation string, connection domain.TradingConnection, cause error) error {
	_, message, err := tradingCredentialStatus(connection.Credentials, cause.Error())
	if err != nil {
		return err
	}
	return fmt.Errorf("trading: %s failed: %s: %w", operation, message, domain.ErrUpstream)
}

func tradingConnectionState(id domain.ExternalID, states map[domain.ExternalID]trading.ConnectionState) (string, string) {
	state, running := states[id]
	if !running {
		return "stopped", ""
	}
	if state.Unavailable != "" {
		return "unavailable", state.Unavailable
	}
	return "running", ""
}

type redactedTradingError struct {
	text  string
	cause error
}

func (e *redactedTradingError) Error() string { return e.text }
func (e *redactedTradingError) Unwrap() error { return e.cause }

func tradingCredentialStatus(credentials, reason string) (map[string]bool, string, error) {
	secrets, redact, err := tradingCredentialRedactor(credentials)
	if err != nil {
		return nil, "", err
	}
	return secrets, redact.Replace(reason), nil
}

func tradingCredentialRedactor(credentials string) (map[string]bool, *strings.Replacer, error) {
	var values map[string]any
	if err := json.Unmarshal([]byte(credentials), &values); err != nil || values == nil {
		return nil, nil, fmt.Errorf("stored trading credentials are not a JSON object: %w", domain.ErrInvalid)
	}
	secrets := make(map[string]bool)
	var credentialStrings []string
	var redact func(any)
	redact = func(value any) {
		switch v := value.(type) {
		case string:
			// A blank value holds no secret; redacting it would garble the
			// whitespace of every message that names the refused member.
			if strings.TrimSpace(v) != "" {
				credentialStrings = append(credentialStrings, v)
			}
		case []any:
			for _, item := range v {
				redact(item)
			}
		case map[string]any:
			for _, item := range v {
				redact(item)
			}
		}
	}
	for key, value := range values {
		set := value != nil
		switch v := value.(type) {
		case string:
			set = strings.TrimSpace(v) != ""
		case []any:
			set = len(v) > 0
		case map[string]any:
			set = len(v) > 0
		}
		if set {
			secrets[key] = true
		}
		redact(value)
	}
	sort.Slice(credentialStrings, func(i, j int) bool {
		return len(credentialStrings[i]) > len(credentialStrings[j])
	})
	pairs := make([]string, 0, len(credentialStrings)*2)
	for _, value := range credentialStrings {
		pairs = append(pairs, value, "[redacted]")
	}
	return secrets, strings.NewReplacer(pairs...), nil
}
