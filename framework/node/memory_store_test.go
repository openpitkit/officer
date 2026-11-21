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
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

var (
	_ store.Store      = (*memoryStore)(nil)
	_ store.RealmStore = (*memoryRealm)(nil)
)

type memoryStore struct {
	executionPolicyLookup domain.ExecutionPolicyProviderLookup
	mu                    sync.Mutex
	realm                 *memoryRealm
	path                  string
	version               int
	closed                bool
	resetErr              error
	resetCommittedErr     error
}

func (s *memoryStore) BindExecutionPolicyProviders(classifier domain.ExecutionPolicyProviderLookup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if classifier == nil {
		return errors.New("store: nil execution-policy provider lookup")
	}
	if s.executionPolicyLookup != nil {
		return errors.New("store: execution-policy provider lookup already bound")
	}
	s.executionPolicyLookup = classifier
	return nil
}

func (r *memoryRealm) GetTestExecutionPolicy(context.Context, domain.ExternalID) (domain.TestExecutionPolicyState, error) {
	return domain.TestExecutionPolicyState{}, errors.New("memory store: test execution policy is not implemented")
}

func (r *memoryRealm) ReplaceTestExecutionPolicy(context.Context, domain.ExternalID, domain.TestExecutionPolicy) (domain.TestExecutionPolicyState, domain.TestExecutionPolicyState, error) {
	return domain.TestExecutionPolicyState{}, domain.TestExecutionPolicyState{}, errors.New("memory store: test execution policy is not implemented")
}

type memoryRealm struct {
	mu    sync.RWMutex
	store *memoryStore

	nextID uint64

	assets       map[string]domain.Asset
	assetClasses map[string]domain.AssetClass
	principals   map[string]domain.Principal
	groups       map[string]domain.AccountGroup
	accounts     map[domain.AccountID]domain.Account
	balances     map[string]domain.Balance

	rateLimits               map[domain.LimitKey]domain.LimitRate
	orderSizeLimits          map[domain.LimitKey]domain.LimitOrderSize
	positionLimits           map[domain.LimitKey]domain.LimitPosition
	spotFundsPnlBoundsLimits map[domain.LimitKey]domain.LimitSpotFundsPnlBounds

	adjustments  []domain.AccountAdjustmentRecord
	orders       map[domain.ExternalID]domain.Order
	orderReviews map[domain.ExternalID]domain.OrderReview
	reports      map[domain.ExternalID]domain.ExternalID
	attestations map[domain.ExternalID]domain.EventAttestation
	events       []domain.OrderEvent
	trades       []domain.Trade
	audit        []domain.AuditRow

	instances   map[domain.ExternalID]domain.MarketDataInstance
	instruments map[string]domain.MarketDataInstrument

	credentialsSealed  bool
	tradingConnections map[domain.ExternalID]domain.TradingConnection
	tradingInstruments map[string]domain.TradingInstrument
	tradingAccess      []domain.TradingAccess

	signingKeys   map[string]domain.SigningKey
	settings      map[string]string
	mcpAccess     map[string]bool
	nextAccountID domain.EngineAccountID
	nextAssetID   domain.EngineAssetID
	nextGroupID   int
}

func newMemoryStore(path string) *memoryStore {
	st := &memoryStore{path: path, version: 1}
	st.realm = newMemoryRealm(st)
	return st
}

func bindMemoryTradingProviders(t *testing.T, st *memoryStore, classes map[string]bool) {
	t.Helper()
	if err := st.BindExecutionPolicyProviders(func(provider string) (bool, error) {
		class, ok := classes[provider]
		if !ok {
			return false, fmt.Errorf("provider %q is not bound in fixture: %w", provider, domain.ErrTradingProviderUnavailable)
		}
		return class, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (r *memoryRealm) validateTradingAccessModes(account domain.Account, grants []domain.TradingAccess) error {
	var modes []domain.TradingMode
	for _, grant := range grants {
		if grant.Account == account.Code || (account.GroupCode != "" && grant.AccountGroup == account.GroupCode) {
			connection, ok := r.tradingConnections[grant.Connection]
			if !ok {
				return fmt.Errorf("trading connection %s for account %s: %w", grant.Connection, account.Code, domain.ErrNotFound)
			}
			modes = append(modes, connection.Mode)
		}
	}
	return domain.ValidateTradingAccessModes(account.Code, modes)
}

func newMemoryRealm(st *memoryStore) *memoryRealm {
	return &memoryRealm{
		store:                    st,
		assets:                   map[string]domain.Asset{},
		assetClasses:             map[string]domain.AssetClass{},
		principals:               map[string]domain.Principal{},
		groups:                   map[string]domain.AccountGroup{},
		accounts:                 map[domain.AccountID]domain.Account{},
		balances:                 map[string]domain.Balance{},
		rateLimits:               map[domain.LimitKey]domain.LimitRate{},
		orderSizeLimits:          map[domain.LimitKey]domain.LimitOrderSize{},
		positionLimits:           map[domain.LimitKey]domain.LimitPosition{},
		spotFundsPnlBoundsLimits: map[domain.LimitKey]domain.LimitSpotFundsPnlBounds{},
		orders:                   map[domain.ExternalID]domain.Order{},
		orderReviews:             map[domain.ExternalID]domain.OrderReview{},
		reports:                  map[domain.ExternalID]domain.ExternalID{},
		attestations:             map[domain.ExternalID]domain.EventAttestation{},
		instances:                map[domain.ExternalID]domain.MarketDataInstance{},
		instruments:              map[string]domain.MarketDataInstrument{},
		tradingConnections:       map[domain.ExternalID]domain.TradingConnection{},
		tradingInstruments:       map[string]domain.TradingInstrument{},
		signingKeys:              map[string]domain.SigningKey{},
		settings:                 map[string]string{},
		mcpAccess:                map[string]bool{},
	}
}

func (s *memoryStore) ForRealm(
	_ context.Context, realm domain.RealmID,
) (store.RealmStore, error) {
	if realm != "" && realm != domain.DefaultRealm {
		return nil, fmt.Errorf("unknown realm %q: %w", realm, domain.ErrInvalid)
	}
	return s.realm, nil
}

func (s *memoryStore) Migrate(context.Context) error { return nil }

func (s *memoryStore) SchemaVersion(context.Context) (int, error) {
	return s.version, nil
}

func (s *memoryStore) Ping(context.Context) error { return nil }

func (s *memoryStore) Path() string { return s.path }

func (s *memoryStore) Reset(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetErr != nil {
		return s.resetErr
	}
	s.realm = newMemoryRealm(s)
	return s.resetCommittedErr
}

func (s *memoryStore) Close() error {
	s.closed = true
	return nil
}

func (r *memoryRealm) nextExternalID() domain.ExternalID {
	r.nextID++
	return domain.ExternalID(fmt.Sprintf("mem-%016x", r.nextID))
}

func balanceKey(account domain.AccountID, asset string) string {
	return string(account) + "\x00" + asset
}

func instrumentKey(instance domain.ExternalID, externalSymbol string) string {
	return instance.String() + "\x00" + externalSymbol
}

func policyRowKey(row store.PolicyListRow) string {
	var mode domain.RuleMode
	if row.OrderSize != nil {
		mode = row.OrderSize.Mode
	}
	if row.Rate != nil {
		mode = row.Rate.Mode
	}
	if row.Position != nil {
		mode = row.Position.Mode
	}
	if row.SpotFundsPnlBounds != nil {
		mode = row.SpotFundsPnlBounds.Mode
	}
	return string(row.Kind) + "\x00" + string(row.Scope) + "\x00" +
		string(row.Account) + "\x00" + row.AccountGroup + "\x00" + row.Asset + "\x00" + string(mode)
}

func pageRows[T any](rows []T, page store.PageSpec) []T {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func statusMatches(filter store.StatusFilter, blocked bool) bool {
	switch filter {
	case store.StatusFilterActive:
		return !blocked
	case store.StatusFilterBlocked:
		return blocked
	default:
		return true
	}
}

func countMatches(filter store.CountRangeFilter, value int) bool {
	if filter.Empty() {
		return true
	}
	if filter.Equal != nil && value != *filter.Equal {
		return false
	}
	if filter.NotEqual != nil && value == *filter.NotEqual {
		return false
	}
	if filter.Min != nil {
		if filter.MinExclusive && value <= *filter.Min {
			return false
		}
		if !filter.MinExclusive && value < *filter.Min {
			return false
		}
	}
	if filter.Max != nil {
		if filter.MaxExclusive && value >= *filter.Max {
			return false
		}
		if !filter.MaxExclusive && value > *filter.Max {
			return false
		}
	}
	return true
}

func decimalMatches(filter store.DecimalRangeFilter, value string) bool {
	if filter.Empty() {
		return true
	}
	if filter.Equal != nil && compareDecimalText(value, *filter.Equal) != 0 {
		return false
	}
	if filter.NotEqual != nil && compareDecimalText(value, *filter.NotEqual) == 0 {
		return false
	}
	if filter.Min != nil {
		cmp := compareDecimalText(value, *filter.Min)
		if filter.MinExclusive && cmp <= 0 {
			return false
		}
		if !filter.MinExclusive && cmp < 0 {
			return false
		}
	}
	if filter.Max != nil {
		cmp := compareDecimalText(value, *filter.Max)
		if filter.MaxExclusive && cmp >= 0 {
			return false
		}
		if !filter.MaxExclusive && cmp > 0 {
			return false
		}
	}
	return true
}

// denominatedDecimalMatches mirrors the SQL connector: a threshold only applies
// to a row carrying the same denomination, so a row in another currency is
// excluded instead of compared against bounds that do not describe it.
func denominatedDecimalMatches(
	filter store.DenominatedDecimalRangeFilter,
	value string,
	currency string,
) bool {
	if filter.Empty() {
		return true
	}
	if filter.Currency != currency {
		return false
	}
	return decimalMatches(filter.Range, value)
}

func timeMatches(filter store.TimeRangeFilter, value time.Time) bool {
	if filter.Empty() {
		return true
	}
	if filter.Min != nil {
		if filter.MinExclusive && !value.After(*filter.Min) {
			return false
		}
		if !filter.MinExclusive && value.Before(*filter.Min) {
			return false
		}
	}
	if filter.Max != nil {
		if filter.MaxExclusive && !value.Before(*filter.Max) {
			return false
		}
		if !filter.MaxExclusive && value.After(*filter.Max) {
			return false
		}
	}
	return true
}

func compareDecimalText(left, right string) int {
	leftDecimal, leftOK := new(big.Rat).SetString(left)
	rightDecimal, rightOK := new(big.Rat).SetString(right)
	if leftOK && rightOK {
		return leftDecimal.Cmp(rightDecimal)
	}
	return strings.Compare(left, right)
}

func compareStrings(left, right string) int {
	return strings.Compare(left, right)
}

func compareInts(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareBools(left, right bool) int {
	switch {
	case left == right:
		return 0
	case !left:
		return -1
	default:
		return 1
	}
}

func compareTimes(left, right time.Time) int {
	switch {
	case left.Before(right):
		return -1
	case left.After(right):
		return 1
	default:
		return 0
	}
}

func sortCompare(cmp int, descending bool) bool {
	if descending {
		return cmp > 0
	}
	return cmp < 0
}

func textMatches(matcher store.TextMatcher, value string) bool {
	if matcher.Empty() {
		return true
	}
	value = strings.ToLower(value)
	if matcher.AnchorStart && matcher.AnchorEnd && len(matcher.Fragments) == 1 {
		return value == strings.ToLower(matcher.Fragments[0])
	}
	pos := 0
	for i, fragment := range matcher.Fragments {
		fragment = strings.ToLower(fragment)
		idx := strings.Index(value[pos:], fragment)
		if idx < 0 {
			return false
		}
		if i == 0 && matcher.AnchorStart && idx != 0 {
			return false
		}
		pos += idx + len(fragment)
	}
	if matcher.AnchorEnd && len(matcher.Fragments) > 0 {
		return strings.HasSuffix(
			value,
			strings.ToLower(matcher.Fragments[len(matcher.Fragments)-1]),
		)
	}
	return true
}

func textMatchesAny(matcher store.TextMatcher, values ...string) bool {
	if matcher.Empty() {
		return true
	}
	for _, value := range values {
		if textMatches(matcher, value) {
			return true
		}
	}
	return false
}

// restrictionMatches mirrors the SQLite account restriction: the zero
// restriction keeps every row; otherwise a row is kept when its account is the
// restricted one or, with IncludeUnowned, when it has no account.
func restrictionMatches(
	restriction domain.AccountRestriction, account domain.AccountID,
) bool {
	return !restriction.Restricted() || account == restriction.Account ||
		(restriction.IncludeUnowned && account == "")
}

func memoryRestrictionSupported(restriction domain.AccountRestriction) error {
	if restriction.Selection != nil {
		return fmt.Errorf("memory store: account selection is unsupported: %w", domain.ErrForbidden)
	}
	return nil
}
