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

package fixture

// Shape cases: zero error results need a syntactically non-nil error.
func zeroErrorResult(err error) (LimitKey, error) {
	// ruleid: limit-key-literal
	return LimitKey{}, err
}

func qualifiedZeroErrorResult(err error) (domain.LimitKey, error) {
	// ruleid: limit-key-literal
	return domain.LimitKey{}, err
}

func formattedZeroErrorResult() (LimitKey, error) {
	// ok: limit-key-literal
	return LimitKey{}, fmt.Errorf("invalid key")
}

func qualifiedFormattedZeroErrorResult() (domain.LimitKey, error) {
	// ok: limit-key-literal
	return domain.LimitKey{}, fmt.Errorf("invalid key")
}

func validationZeroErrorResult() (LimitKey, error) {
	// ok: limit-key-literal
	return LimitKey{}, NewValidationError("/scope", "invalid", "invalid scope")
}

func qualifiedValidationZeroErrorResult() (domain.LimitKey, error) {
	// ok: limit-key-literal
	return domain.LimitKey{}, domain.NewValidationError("/scope", "invalid", "invalid scope")
}

func acceptedZeroResult() (LimitKey, error) {
	// ruleid: limit-key-literal
	return LimitKey{}, nil
}

func qualifiedAcceptedZeroResult() (domain.LimitKey, error) {
	// ruleid: limit-key-literal
	return domain.LimitKey{}, nil
}

func nonemptyErrorResult(err error) (LimitKey, error) {
	// ruleid: limit-key-literal
	return LimitKey{policy: policy}, err
}

func qualifiedNonemptyErrorResult(err error) (domain.LimitKey, error) {
	// ruleid: limit-key-literal
	return domain.LimitKey{policy: policy}, err
}

func zeroUses(key domain.LimitKey) {
	// ruleid: limit-key-literal
	k := domain.LimitKey{}
	// ruleid: limit-key-literal
	var local = LimitKey{}
	// ruleid: limit-key-literal
	keys := map[domain.LimitKey]bool{domain.LimitKey{}: true}
	// ruleid: limit-key-literal
	present := keys[domain.LimitKey{}]
	// ruleid: limit-key-literal
	same := key == domain.LimitKey{}
	// ruleid: limit-key-literal
	consume(domain.LimitKey{})
	// ruleid: limit-key-literal
	value := struct{ key domain.LimitKey }{key: domain.LimitKey{}}
	_, _, _, _, _, _ = k, local, keys, present, same, value
}

// Shape negatives from the scan: diagnostics, prose and query composition
// mention axes but never assemble a barrier identity.
func axisDiagnostics(limit domain.LimitOrderSize, mode domain.RuleMode, name string) {
	// ok: limit-key-string-join
	label := fmt.Sprintf("account %q asset %q", limit.Account, limit.Asset)
	// ok: limit-key-string-join
	message := string(mode) + " mode takes no " + name
	// ok: limit-key-string-join
	description := "An order-size barrier also carries " +
		"read-only mode: reject, review_without_hold or review_with_hold. " +
		"mode is not a write argument "
	// ok: limit-key-string-join
	err := fmt.Errorf("store: restore %s scope %q "+
		"account %q asset %q: %w", policy, scope, account, asset, cause)
	_, _, _, _ = label, message, description, err
}

func queryComposition(ctx context.Context, modeColumn, table string) {
	// ok: limit-key-string-join
	rows, err := tx.QueryContext(ctx,
		`SELECT t.id, t.scope, a.code, ast.code, `+modeColumn+`
		 FROM `+table+` t
		 LEFT JOIN account a   ON a.id   = t.account_id
		 LEFT JOIN asset   ast ON ast.id = t.asset_id`)
	_, _ = rows, err
}

// Negative source: framework/node/local_limits.go:111; audit field rendering.
func renderAxis(axis domain.AuditDetailField) string {
	address := ""
	// ok: limit-key-string-join
	address += fmt.Sprintf(" %s %q", axis.Name, axis.Value)
	return address
}

// A handler signature alone must not exempt a non-request target.
func nonRequestTarget(svc backend.ControlPlane) http.HandlerFunc {
	// ruleid: limit-target-literal
	target := node.LimitTarget{Policy: policy, Scope: scope, Account: account}
	return func(w http.ResponseWriter, r *http.Request) { consume(target) }
}

// Review shape cases. These are synthetic, not historical sites.
func nilErrorKey() (domain.LimitKey, error) {
	var err error
	// ruleid: limit-key-literal
	return domain.LimitKey{}, err
}

func guardedErrorKey(err error) (domain.LimitKey, error) {
	if err != nil {
		// ok: limit-key-literal
		return domain.LimitKey{}, err
	}
	return domain.NewLimitKey(policy, mode, scope, account, group, asset)
}

func newErrorKey() (domain.LimitKey, error) {
	// ok: limit-key-literal
	return domain.LimitKey{}, errors.New("invalid key")
}

func wrappedErrorKey(err error) (domain.LimitKey, error) {
	// ruleid: limit-key-literal
	return domain.LimitKey{}, wrapError(err)
}

func joinedRateKey(l domain.LimitRate) string {
	// ruleid: limit-key-string-join
	return fmt.Sprintf("rate:%s:%s:%s", l.Scope, l.Account, l.Asset)
}

func targetAxes(t node.LimitTarget, u node.LimitTarget) {
	// ruleid: limit-key-string-join
	key := t.Account.String() + "\x00" + t.Asset
	// ruleid: limit-key-axis-compare
	same := t.Account == u.Account && t.Asset == u.Asset
	consume(key, same)
}

func pointerAxes(l, t *domain.LimitRate) {
	// ruleid: limit-key-string-join
	key := l.Account.String() + "\x00" + l.Asset
	// ruleid: limit-key-axis-compare
	same := l.Account == t.Account && l.Asset == t.Asset
	consume(key, same)
}

func rangedAxes(limits []domain.LimitRate) {
	for _, l := range limits {
		// ruleid: limit-key-string-join
		key := l.Account.String() + "\x00" + l.Asset
		// ruleid: limit-key-axis-compare
		same := l.Account == target.Account && l.Asset == target.Asset
		// ruleid: limit-key-string-join
		formatted := fmt.Sprintf("%s:%s", l.Account, l.Asset)
		// ruleid: limit-key-string-join
		joined := strings.Join([]string{l.Account.String(), l.Asset}, ":")
		// ruleid: limit-key-composite
		keep[barrierKey{l.Account, l.Asset}] = true
		consume(formatted, joined)
		consume(key, same)
	}
}

func unrelatedRangeOperand(limits []domain.LimitRate, b backup.Balance) {
	for _, l := range limits {
		// ok: limit-key-string-join
		key := b.Account.String() + "\x00" + b.Asset
		// ok: limit-key-composite
		holdings[holdingKey{b.Account, b.Asset}] = true
		consume(l, key)
	}
}

func unrelatedLimitArgument(b backup.Balance, unused domain.LimitPosition) {
	// ok: limit-key-string-join
	key := b.Account.String() + "\x00" + b.Asset
	consume(key)
}

func unrelatedScopeArgument(a, b domain.TradingAccess, unused domain.LimitScope) bool {
	// ok: limit-key-axis-compare
	return a.Account == b.Account && a.AccountGroup == b.AccountGroup
}

func reversedAxes(a, b domain.LimitRate) bool {
	// ruleid: limit-key-axis-compare
	return a.Scope == b.Scope && b.Account == a.Account
}

func differentAxes(limits []domain.LimitRate, t domain.LimitRate) {
	for _, l := range limits {
		// ruleid: limit-key-axis-compare
		if l.Scope != t.Scope || l.Account != t.Account {
			continue
		}
		consume(l)
	}
}

func guardedAxisComparisons(l domain.LimitSpotFundsPnlBounds, t node.LimitTarget, ok bool) {
	// ruleid: limit-key-axis-compare
	byPolicy := l.Axis.Policy() == t.Policy && l.Scope == t.Scope && l.Account == t.Account
	// ruleid: limit-key-axis-compare
	byGuard := ok && l.Scope == t.Scope && l.Account == t.Account
	consume(byPolicy, byGuard)
}

func scalarAxisComparisons(l domain.LimitRate, scope domain.LimitScope, account domain.AccountID) bool {
	// ruleid: limit-key-axis-compare
	return l.Scope == scope && l.Account == account
}

func compositeKeys(l domain.LimitRate, t domain.LimitRate) {
	// ruleid: limit-key-composite
	keep[barrierKey{l.Scope, l.Account, l.Asset}] = true
	// ruleid: limit-key-composite
	keep[barrierKey{scope: l.Scope, account: l.Account, asset: l.Asset}] = true
	// ruleid: limit-key-composite
	keep[struct {
		scope   domain.LimitScope
		account domain.AccountID
	}{l.Scope, l.Account}] = true
	// ruleid: limit-key-composite
	keep[[3]string{string(l.Scope), l.Account.String(), l.Asset}] = true
	// ruleid: limit-key-composite
	equal := stored == barrierKey{l.Scope, l.Account, l.Asset}
	consume(equal)
}

func compositeNegatives(b backup.Balance, l domain.LimitRate) {
	// ok: limit-key-composite
	holdings[holdingKey{b.Account, b.Asset}] = true
	// ok: limit-key-composite
	keep[barrierKey{scope: l.Scope}] = true
	// ok: limit-key-axis-compare
	visible := l.Account == account
	consume(visible)
}

func reorderedSQL() {
	// ruleid: limit-key-sql
	q1 := "account_id IS ? AND scope = ?"
	// ruleid: limit-key-sql
	q2 := "scope = ? AND account_id = ?"
	// ruleid: limit-key-sql
	q3 := "scope = ? AND asset_id IS ?"
	// Mode is a generic column in access_state and trading_connection.
	// ok: limit-key-sql
	q4 := "asset_id = ? AND mode IS ?"
	// ok: limit-key-sql
	q5 := "account_id IS ? AND account_group_id IS ?"
	// ok: limit-key-sql
	q6 := "scope = ?"
	consume(q1, q2, q3, q4, q5, q6)
}

// Synthetic SQL chain boundaries. Existing positives above stay unchanged.
func sqlChainBoundaries() {
	// ok: limit-key-sql
	updateMode := "UPDATE access_state SET mode = ? WHERE account_id = ?"
	// ok: limit-key-sql
	union := `SELECT b.account_id FROM balance b WHERE b.asset_id = ?
		UNION ALL
		SELECT l.account_id FROM limit_rate l WHERE l.scope = ?`
	consume(updateMode, union)
}

// Final review synthetic cases. Verbatim site bodies below are unchanged.
func sentinelKey() (domain.LimitKey, error) {
	// ok: limit-key-literal
	return domain.LimitKey{}, domain.ErrInvalid
}
func addressErrorKey() (domain.LimitKey, error) {
	// ok: limit-key-literal
	return domain.LimitKey{}, &keyError{}
}
func initErrorKey() (domain.LimitKey, error) {
	if err := readError(); err != nil {
		// ok: limit-key-literal
		return domain.LimitKey{}, err
	}
	return domain.NewLimitKey(policy, mode, scope, account, group, asset)
}
func reversedErrorKey(err error) (domain.LimitKey, error) {
	if nil != err {
		// ok: limit-key-literal
		return domain.LimitKey{}, err
	}
	return domain.NewLimitKey(policy, mode, scope, account, group, asset)
}
func combinedErrorKey(err error, valid bool) (domain.LimitKey, error) {
	if err != nil && valid {
		// ok: limit-key-literal
		return domain.LimitKey{}, err
	}
	if valid && err != nil {
		// ok: limit-key-literal
		return domain.LimitKey{}, err
	}
	return domain.NewLimitKey(policy, mode, scope, account, group, asset)
}
func earlySuccessKey(k domain.LimitKey, err error) (domain.LimitKey, error) {
	if err == nil {
		return k, nil
	}
	// Known false positive: the rule does not prove non-nil after an early-success return.
	// todook: limit-key-literal
	return domain.LimitKey{}, err
}
func switchErrorKey(err error) (domain.LimitKey, error) {
	switch {
	case err != nil:
		// Known false positive: switch guards are not recognised by the error-shape exemption.
		// todook: limit-key-literal
		return domain.LimitKey{}, err
	}
	return domain.NewLimitKey(policy, mode, scope, account, group, asset)
}
func parameterIDs(l domain.LimitRate, scope domain.LimitScope, accountID domain.AccountID) bool {
	// ruleid: limit-key-axis-compare
	return l.Scope == scope && l.Account == accountID
}
func parameterAbbreviations(l domain.LimitRate, s domain.LimitScope, aid domain.AccountID) bool {
	// ruleid: limit-key-axis-compare
	return l.Scope == s && l.Account == aid
}
func (m matcher) parameterMethod(l domain.LimitRate, scope domain.LimitScope, accountID domain.AccountID) bool {
	// ruleid: limit-key-axis-compare
	return l.Scope == scope && l.Account == accountID
}

var parameterLiteral = func(l domain.LimitRate, scope domain.LimitScope, accountID domain.AccountID) bool {
	// ruleid: limit-key-axis-compare
	return l.Scope == scope && l.Account == accountID
}

func localScopeFilter(limit domain.LimitSpotFundsPnlBounds, group string) bool {
	scope := domain.ScopeAccountGroup
	// ok: limit-key-axis-compare
	return limit.Scope == scope && limit.AccountGroup == group
}
func constantScopeFilter(limit domain.LimitSpotFundsPnlBounds, group string) bool {
	// ok: limit-key-axis-compare
	return limit.Scope == ScopeAccountGroup && limit.AccountGroup == group
}
func tradingSubjectLocals(a, b domain.TradingAccess) bool {
	aAccount, bAccount := a.Account, b.Account
	aAccountGroup, bAccountGroup := a.AccountGroup, b.AccountGroup
	// ok: limit-key-axis-compare
	return aAccount == bAccount && aAccountGroup == bAccountGroup
}
func accountAssetLocals(fromAccount, toAccount, fromAsset, toAsset string) bool {
	// ok: limit-key-axis-compare
	return fromAccount == toAccount && fromAsset == toAsset
}
func modeAccountLocals(beforeMode, afterMode, beforeAccount, afterAccount string) bool {
	// ok: limit-key-axis-compare
	return beforeMode != afterMode || beforeAccount != afterAccount
}
func inlineEngineKey(limit domain.LimitOrderSize, account param.AccountID, asset param.Asset) {
	// ok: limit-key-composite
	values[orderSizeLimitKey{scope: limit.Scope, account: account, asset: asset.Safe()}] = value
}
func privateEngineCompare(a, b orderSizeLimitKey) bool {
	// ok: limit-key-axis-compare
	return a.scope != b.scope || a.account != b.account
}
func formatConvention(l domain.LimitRate) {
	// ok: limit-key-string-join
	prose := fmt.Sprintf("account %s asset %s", l.Account, l.Asset)
	// ruleid: limit-key-string-join
	key := fmt.Sprintf("account=%s/asset=%s", l.Account, l.Asset)
	consume(prose, key)
}
func indexedLimits(limits []domain.LimitRate) {
	for index, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		consume(index, key)
	}
}
func targetSlice(limits []node.LimitTarget) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		consume(key)
	}
}
func reversedComposite(l domain.LimitRate) bool {
	// ruleid: limit-key-composite
	return barrierKey{l.Scope, l.Account} == stored
}
func deleteComposite(l domain.LimitRate) {
	// ruleid: limit-key-composite
	delete(keep, barrierKey{l.Scope, l.Account})
}

func reversedKeyedComposite(l domain.LimitRate) bool {
	// ruleid: limit-key-composite
	return (barrierKey{scope: l.Scope, account: l.Account}) == stored
}

func deleteKeyedComposite(l domain.LimitRate) {
	// ruleid: limit-key-composite
	delete(keep, barrierKey{scope: l.Scope, account: l.Account})
}

func localLimitRateValue(a, b LimitRate) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func localLimitRatePointer(a, b *LimitRate) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func localLimitPositionValue(a, b LimitPosition) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func localLimitPositionPointer(a, b *LimitPosition) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func localLimitOrderSizeValue(a, b LimitOrderSize) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func localLimitOrderSizePointer(a, b *LimitOrderSize) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func localLimitSpotFundsPnlBoundsValue(a, b LimitSpotFundsPnlBounds) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.AccountGroup == b.AccountGroup
}

func localLimitSpotFundsPnlBoundsPointer(a, b *LimitSpotFundsPnlBounds) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.AccountGroup == b.AccountGroup
}

func localLimitTargetValue(a, b LimitTarget) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func localLimitTargetPointer(a, b *LimitTarget) bool {
	// ruleid: limit-key-axis-compare
	return a.Account == b.Account && a.Asset == b.Asset
}

func pointerSliceLimitRate(limits []*domain.LimitRate) {
	for _, l := range limits {
		// ruleid: limit-key-string-join
		key := l.Account.String() + ":" + l.Asset
		consume(key)
	}
}

func pointerSliceLimitPosition(limits []*domain.LimitPosition) {
	for _, l := range limits {
		// ruleid: limit-key-string-join
		key := l.Account.String() + ":" + l.Asset
		consume(key)
	}
}

func pointerSliceLimitOrderSize(limits []*domain.LimitOrderSize) {
	for _, l := range limits {
		// ruleid: limit-key-string-join
		key := l.Account.String() + ":" + l.Asset
		consume(key)
	}
}

func pointerSliceLimitSpotFundsPnlBounds(limits []*domain.LimitSpotFundsPnlBounds) {
	for _, l := range limits {
		// ruleid: limit-key-string-join
		key := l.Account.String() + ":" + l.AccountGroup
		consume(key)
	}
}

// Final Go prose cases in a covered limit context.
func goProseKeys(l domain.LimitRate) {
	// todook: limit-key-string-join
	plus := string(l.Scope) + " for " + l.Account.String()
	// todook: limit-key-string-join
	joined := strings.Join([]string{string(l.Scope), l.Account.String()}, " ")
	consume(plus, joined)
}

// Predicate-level OR cases for the SQL chain guard.
func predicateLevelOr() {
	// todook: limit-key-sql
	where := "SELECT * FROM limit_rate WHERE (scope = ? OR mode IS NULL AND account_id = ?)"
	// todook: limit-key-sql
	subquery := "SELECT * FROM limit_rate WHERE id IN (SELECT id FROM t WHERE scope = ? OR x = 1 AND account_id = ?)"
	consume(where, subquery)
}

// Reversed inequality orientations, independent of the symmetric_eq option.
func reversedMemberInequalities(a, b domain.LimitRate) {
	// ruleid: limit-key-axis-compare
	first := b.Scope != a.Scope || a.Account != b.Account
	// ruleid: limit-key-axis-compare
	second := a.Scope != b.Scope || b.Account != a.Account
	// ruleid: limit-key-axis-compare
	both := b.Scope != a.Scope || b.Account != a.Account
	consume(first, second, both)
}

func reversedScalarInequalities(l domain.LimitRate, scope domain.LimitScope, account domain.AccountID) {
	// ruleid: limit-key-axis-compare
	first := scope != l.Scope || l.Account != account
	// ruleid: limit-key-axis-compare
	second := l.Scope != scope || account != l.Account
	// ruleid: limit-key-axis-compare
	both := scope != l.Scope || account != l.Account
	consume(first, second, both)
}

// Value-qualified error fields and their guard exemptions.
type sentinelState struct{ ErrCause error }

func (res sentinelState) receiverSentinel() (domain.LimitKey, error) {
	// ruleid: limit-key-literal
	return domain.LimitKey{}, res.ErrCause
}

func (m matcher) parameterSentinel(res sentinelState) (domain.LimitKey, error) {
	// ruleid: limit-key-literal
	return domain.LimitKey{}, res.ErrCause
}

var literalSentinel = func(res sentinelState) (domain.LimitKey, error) {
	// ruleid: limit-key-literal
	return domain.LimitKey{}, res.ErrCause
}

func guardedSentinelFields(res sentinelState, valid bool) (domain.LimitKey, error) {
	if res.ErrCause != nil {
		// ok: limit-key-literal
		return domain.LimitKey{}, res.ErrCause
	}
	if nil != res.ErrCause {
		// ok: limit-key-literal
		return domain.LimitKey{}, res.ErrCause
	}
	if ready := valid; res.ErrCause != nil {
		consume(ready)
		// ok: limit-key-literal
		return domain.LimitKey{}, res.ErrCause
	}
	if res.ErrCause != nil && valid {
		// ok: limit-key-literal
		return domain.LimitKey{}, res.ErrCause
	}
	if valid && res.ErrCause != nil {
		// ok: limit-key-literal
		return domain.LimitKey{}, res.ErrCause
	}
	return domain.NewLimitKey(policy, mode, scope, account, group, asset)
}

var packageError sentinelState

func packageSentinelField() (domain.LimitKey, error) {
	// Known miss: the qualifier is a package-level value rather than a parameter or receiver.
	// todoruleid: limit-key-literal
	return domain.LimitKey{}, packageError.ErrCause
}

// Close-iteration synthetic cases; historical and verbatim sources below are unchanged.
func closeSQLChains() {
	// ok: limit-key-sql
	having := "scope = ? GROUP BY asset_id HAVING count(*) > 0 AND account_id = ?"
	// ok: limit-key-sql
	on := "scope = ? ON x = y AND account_id = ?"
	// ok: limit-key-sql
	join := "scope = ? JOIN account a ON a.id = x AND account_id = ?"
	// ok: limit-key-sql
	unparenthesised := "scope = ? OR mode IS ? AND account_id = ?"
	// ruleid: limit-key-sql
	groupBetween := "scope = ? AND (mode IS ? OR mode IS NULL) AND account_id = ?"
	// ruleid: limit-key-sql
	subjectInGroup := "scope = ? AND (mode IS ? OR account_id = ?)"
	// ruleid: limit-key-sql
	scopeInGroup := "(scope = ? OR mode IS NULL) AND account_id = ?"
	// ruleid: limit-key-sql
	nested := "scope = ? AND (mode IS ? OR (axis IS ? OR mode IS NULL)) AND account_id = ?"
	// ruleid: limit-key-sql
	reversed := "account_id = ? AND (mode IS ? OR mode IS NULL) AND scope = ?"
	consume(having, on, join, unparenthesised, groupBetween, subjectInGroup, scopeInGroup, nested, reversed)
}

func closeDerivedOperands(a, b domain.LimitRate, scope domain.LimitScope, account domain.AccountID) {
	// todook: limit-key-axis-compare
	ranks := scopeRank(a.Scope) == scopeRank(b.Scope) && a.Account == b.Account
	// todook: limit-key-axis-compare
	lengths := len(a.Account) == len(b.Account) && a.Asset == b.Asset
	// todook: limit-key-axis-compare
	scalarRank := scopeRank(a.Scope) == scope && a.Account == account
	// todook: limit-key-composite
	emptiness[emptinessKey{a.Scope == "", a.Account == ""}] = true
	// todook: limit-key-composite
	derived[rankKey{scopeRank(a.Scope), a.Account}] = true
	// ruleid: limit-key-axis-compare
	wrappers := string(a.Scope) == b.Scope.String() && a.Account.String() == string(b.Account)
	// ruleid: limit-key-axis-compare
	different := b.Scope.String() != string(a.Scope) || string(a.Account) != b.Account.String()
	// ruleid: limit-key-composite
	keep[barrierKey{string(a.Scope), a.Account.String()}] = true
	consume(ranks, lengths, scalarRank, wrappers, different)
}

func closeGroupedParameters(l domain.LimitRate, scope, account string) bool {
	// ruleid: limit-key-axis-compare
	return l.Scope == scope && l.Account == account
}
func closeGenericParameters[Context any](l domain.LimitRate, scope domain.LimitScope, account domain.AccountID, context Context) bool {
	// ruleid: limit-key-axis-compare
	return l.Scope == scope && l.Account == account
}
func closeMultiResultParameters(l domain.LimitRate, scope domain.LimitScope, account domain.AccountID) (bool, error) {
	// ruleid: limit-key-axis-compare
	same := l.Scope == scope && l.Account == account
	return same, nil
}
func (m matcher) closeMultiResultMethod(l domain.LimitRate, scope domain.LimitScope, account domain.AccountID) (bool, error) {
	// ruleid: limit-key-axis-compare
	same := l.Scope == scope && l.Account == account
	return same, nil
}

func closeSentinelField(res struct{ ErrCause error }) (domain.LimitKey, error) {
	// Parameter-qualified fields are values rather than package sentinels.
	// ruleid: limit-key-literal
	return domain.LimitKey{}, res.ErrCause
}
func closeMixedPair(l domain.LimitRate, target node.LimitTarget, account domain.AccountID) bool {
	// Known miss: accepting one member and one parameter needs a new member-position/signature product.
	// todoruleid: limit-key-axis-compare
	return l.Scope == target.Scope && l.Account == account
}

func closeSlice0(limits []LimitTarget) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		// ruleid: limit-key-axis-compare
		same := limit.Account == target.Account && limit.Asset == target.Asset
		// ruleid: limit-key-composite
		keep[barrierKey{limit.Account, limit.Asset}] = true
		consume(key, same)
	}
}

func closeSlice1(limits []*node.LimitTarget) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		// ruleid: limit-key-axis-compare
		same := limit.Account == target.Account && limit.Asset == target.Asset
		// ruleid: limit-key-composite
		keep[barrierKey{limit.Account, limit.Asset}] = true
		consume(key, same)
	}
}

func closeSlice2(limits []*LimitRate) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		// ruleid: limit-key-axis-compare
		same := limit.Account == target.Account && limit.Asset == target.Asset
		// ruleid: limit-key-composite
		keep[barrierKey{limit.Account, limit.Asset}] = true
		consume(key, same)
	}
}

func closeSlice3(limits []*LimitPosition) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		// ruleid: limit-key-axis-compare
		same := limit.Account == target.Account && limit.Asset == target.Asset
		// ruleid: limit-key-composite
		keep[barrierKey{limit.Account, limit.Asset}] = true
		consume(key, same)
	}
}

func closeSlice4(limits []*LimitOrderSize) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		// ruleid: limit-key-axis-compare
		same := limit.Account == target.Account && limit.Asset == target.Asset
		// ruleid: limit-key-composite
		keep[barrierKey{limit.Account, limit.Asset}] = true
		consume(key, same)
	}
}

func closeSlice5(limits []*LimitSpotFundsPnlBounds) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.AccountGroup
		// ruleid: limit-key-axis-compare
		same := limit.Account == target.Account && limit.AccountGroup == target.AccountGroup
		// ruleid: limit-key-composite
		keep[barrierKey{limit.Account, limit.AccountGroup}] = true
		consume(key, same)
	}
}

func closeSlice6(limits []*LimitTarget) {
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		key := limit.Account.String() + ":" + limit.Asset
		// ruleid: limit-key-axis-compare
		same := limit.Account == target.Account && limit.Asset == target.Asset
		// ruleid: limit-key-composite
		keep[barrierKey{limit.Account, limit.Asset}] = true
		consume(key, same)
	}
}

// Historical functions below are copied from 9d7fe3b1. They are parsed, not
// type-checked; only annotations are added to their bodies.

// Source: framework/node/local_limits.go:123 at 9d7fe3b1.
func (n *localNode) PutRateLimit(
	ctx context.Context, limit domain.LimitRate, write domain.LimitWrite,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()

	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy:  domain.PolicyRateLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	prev, hadPrev, err := n.readRateBarrier(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := checkLimitWrite(write, target, hadPrev); err != nil {
		return nil, err
	}
	// Every matching rate barrier applies, so a tighten-only caller may add one
	// under a new key, the policy's first included: with no rate barrier
	// configured, the engine rebuild that installs it resets no rate window.
	// A barrier under an existing key replaces that one, so it must be no
	// looser.
	if caller.TightenOnly() && hadPrev {
		if err := limit.NoLooserThan(prev); err != nil {
			return nil, fmt.Errorf("put rate limit: %w", err)
		}
	}
	if err := n.ensureLimitAccount(
		ctx, limit.Scope, limit.Account, missing, "put rate limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "rate limit", caller,
	); err != nil {
		return nil, err
	}
	audit, err := n.captureAuditTarget(ctx, store.AuditEntry{
		Action: domain.AuditActionSetLimit, Account: target.Account,
		Detail: setRateLimitDetail(limit),
	})
	if err != nil {
		return nil, err
	}

	snapshot, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("load rate policy lifecycle snapshot: %w", err)
	}
	var sink marketdata.Sink
	if !hadPrev && len(snapshot.RateLimits) == 0 {
		snapshot.RateLimits = []domain.LimitRate{limit}
		durableCtx := context.WithoutCancel(ctx)
		sink, err = n.replaceEngineForPolicyLifecycle(
			ctx, domain.PolicyRateLimit, snapshot, func() error {
				return n.realm.PutRateLimit(durableCtx, limit)
			},
		)
		if err != nil {
			return sink, fmt.Errorf("install first rate limit: %w", err)
		}
	} else {
		if err := n.realm.PutRateLimit(ctx, limit); err != nil {
			return nil, fmt.Errorf("put rate limit: %w", err)
		}
		sink, err = n.applyPolicyChangeLocked(ctx, domain.PolicyRateLimit, func() error {
			return n.revertRateBarrier(context.WithoutCancel(ctx), target, prev, hadPrev)
		})
		if err != nil {
			return sink, fmt.Errorf("configure policy after limit: %w", err)
		}
	}
	ctx = context.WithoutCancel(ctx)
	if err := n.audit(ctx, caller, audit); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit rate limit", "policy", domain.PolicyRateLimit,
			fmt.Errorf("audit set limit: %w", err),
		)
	}
	return sink, nil
}

// Source: framework/node/local_limits.go:213 at 9d7fe3b1.
func (n *localNode) PutOrderSizeLimit(
	ctx context.Context, limit domain.LimitOrderSize, write domain.LimitWrite,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()

	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy:  domain.PolicyOrderSizeLimit,
		Mode:    limit.Mode,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	prev, hadPrev, err := n.readOrderSizeBarrier(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := checkLimitWrite(write, target, hadPrev); err != nil {
		return nil, err
	}
	if err := n.guardTightenOnlyOrderSize(
		ctx, limit, prev, hadPrev, caller,
	); err != nil {
		return nil, fmt.Errorf("put order-size limit: %w", err)
	}
	if err := n.ensureLimitAccount(
		ctx, limit.Scope, limit.Account, missing, "put order-size limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "order-size limit", caller,
	); err != nil {
		return nil, err
	}
	audit, err := n.captureAuditTarget(ctx, store.AuditEntry{
		Action: domain.AuditActionSetLimit, Account: target.Account,
		Detail: setOrderSizeLimitDetail(limit),
	})
	if err != nil {
		return nil, err
	}

	snapshot, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("load order-size policy lifecycle snapshot: %w", err)
	}
	var sink marketdata.Sink
	if orderSizeModeCount(snapshot.OrderSizeLimits, limit.Mode) == 0 {
		snapshot.OrderSizeLimits = append(snapshot.OrderSizeLimits, limit)
		durableCtx := context.WithoutCancel(ctx)
		sink, err = n.replaceEngineForPolicyLifecycle(
			ctx, domain.PolicyOrderSizeLimit, snapshot, func() error {
				return n.realm.PutOrderSizeLimit(durableCtx, limit)
			},
		)
		if err != nil {
			return sink, fmt.Errorf("install first order-size limit of mode %q: %w", limit.Mode, err)
		}
	} else {
		if err := n.realm.PutOrderSizeLimit(ctx, limit); err != nil {
			return nil, fmt.Errorf("put order-size limit: %w", err)
		}
		sink, err = n.applyPolicyChangeLocked(
			ctx, domain.PolicyOrderSizeLimit, func() error {
				return n.revertOrderSizeBarrier(
					context.WithoutCancel(ctx), target, prev, hadPrev,
				)
			},
		)
		if err != nil {
			return sink, fmt.Errorf("configure policy after limit: %w", err)
		}
	}
	ctx = context.WithoutCancel(ctx)
	if err := n.audit(ctx, caller, audit); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit order-size limit", "policy", domain.PolicyOrderSizeLimit,
			fmt.Errorf("audit set limit: %w", err),
		)
	}
	return sink, nil
}

// Source: framework/node/local_limits.go:302 at 9d7fe3b1.
func (n *localNode) PutPositionLimit(
	ctx context.Context, limit domain.LimitPosition, write domain.LimitWrite,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()
	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy: domain.PolicyPositionLimit, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset,
	}
	prev, hadPrev, err := n.readPositionBarrier(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := checkLimitWrite(write, target, hadPrev); err != nil {
		return nil, err
	}
	if caller.TightenOnly() && hadPrev {
		if err := limit.NoLooserThan(prev); err != nil {
			return nil, fmt.Errorf("put position limit: %w", err)
		}
	}
	if err := n.ensureLimitAccount(ctx, limit.Scope, limit.Account, missing, "put position limit", caller); err != nil {
		return nil, err
	}
	if err := n.ensureLimitAsset(ctx, limit.Scope, limit.Asset, "position limit", caller); err != nil {
		return nil, err
	}
	audit, err := n.captureAuditTarget(ctx, store.AuditEntry{
		Action: domain.AuditActionSetLimit, Account: target.Account,
		Detail: setPositionLimitDetail(limit),
	})
	if err != nil {
		return nil, err
	}
	if err := n.realm.PutPositionLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put position limit: %w", err)
	}
	sink, err := n.applyPolicyChangeLocked(ctx, domain.PolicyPositionLimit, func() error {
		return n.revertPositionBarrier(context.WithoutCancel(ctx), target, prev, hadPrev)
	})
	if err != nil {
		return sink, fmt.Errorf("configure policy after limit: %w", err)
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, audit); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit position limit", "policy", domain.PolicyPositionLimit,
			fmt.Errorf("audit set limit: %w", err),
		)
	}
	return sink, nil
}

// Source: framework/node/local_limits.go:363 at 9d7fe3b1.
func (n *localNode) PutSpotFundsPnlBoundsLimit(
	ctx context.Context, limit domain.LimitSpotFundsPnlBounds, write domain.LimitWrite,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()

	policy := limit.Axis.Policy()
	if policy == "" {
		return nil, fmt.Errorf(
			"unknown spot funds pnl axis %q: %w", limit.Axis, domain.ErrInvalid,
		)
	}
	var groupCode, currency string
	checkAccountAndCurrency := func() error {
		if limit.Scope != domain.ScopeAccount {
			return nil
		}
		if missing != domain.MissingAccountCreate {
			if err := n.ensureLimitAccount(
				ctx, limit.Scope, limit.Account, missing,
				"put spot-funds P&L bounds limit", caller,
			); err != nil {
				return err
			}
		}
		var err error
		groupCode, currency, err = n.spotFundsPnlAccountCurrency(ctx, limit.Account)
		if err != nil {
			return err
		}
		if currency == "" || currency != limit.Currency {
			return domain.NewPnlBoundCurrencyMismatchError(
				limit.Account, currency, limit.Currency,
			)
		}
		return nil
	}
	// Preserve Replace's currency refusal before the barrier read.
	if write == domain.LimitWriteReplace {
		if err := checkAccountAndCurrency(); err != nil {
			return nil, err
		}
	}
	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy:       policy,
		Scope:        limit.Scope,
		Account:      limit.Account,
		AccountGroup: limit.AccountGroup,
	}
	prev, hadPrev, err := n.readSpotFundsPnlBoundsBarrier(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := checkLimitWrite(write, target, hadPrev); err != nil {
		return nil, err
	}
	if write != domain.LimitWriteReplace {
		if err := checkAccountAndCurrency(); err != nil {
			return nil, err
		}
	}
	if err := n.guardTightenOnlySpotFundsPnlBounds(
		ctx, limit, prev, hadPrev, groupCode, currency, caller,
	); err != nil {
		return nil, fmt.Errorf("put spot funds pnl-bounds limit: %w", err)
	}
	if limit.Scope != domain.ScopeAccount || missing == domain.MissingAccountCreate {
		if err := n.ensureLimitAccount(
			ctx, limit.Scope, limit.Account, missing,
			"put spot-funds P&L bounds limit", caller,
		); err != nil {
			return nil, err
		}
	}

	audit, err := n.captureAuditTarget(ctx, store.AuditEntry{
		Action: domain.AuditActionSetLimit, Account: target.Account, Group: target.AccountGroup,
		Detail: setSpotFundsPnlBoundsLimitDetail(limit),
	})
	if err != nil {
		return nil, err
	}
	if err := n.realm.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put spot funds pnl-bounds limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(
		ctx, policy, func() error {
			return n.revertSpotFundsPnlBoundsBarrier(
				context.WithoutCancel(ctx), target, prev, hadPrev,
			)
		},
	)
	if applyErr != nil {
		return sink, fmt.Errorf("configure policy after limit: %w", applyErr)
	}
	ctx = context.WithoutCancel(ctx)

	if err := n.audit(ctx, caller, audit); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit spot funds pnl-bounds limit", "policy",
			policy,
			fmt.Errorf("audit set limit: %w", err),
		)
	}
	return sink, nil
}

// Source: framework/node/local_limits.go:485 at 9d7fe3b1.
func (n *localNode) guardTightenOnlyOrderSize(
	ctx context.Context,
	limit, prev domain.LimitOrderSize,
	hadPrev bool,
	caller domain.Caller,
) error {
	if !caller.TightenOnly() {
		return nil
	}
	if hadPrev {
		return limit.NoLooserThan(prev)
	}
	limits, err := n.realm.ListOrderSizeLimits(ctx, "")
	if err != nil {
		return fmt.Errorf("read order-size barriers: %w", err)
	}
	if orderSizeModeCount(limits, limit.Mode) == 0 {
		return n.guardTightenOnlyRebuild(ctx, caller)
	}
	var overridden domain.LimitScope
	switch limit.Scope {
	case domain.ScopeAccountUnderlyingAsset:
		overridden = domain.ScopeUnderlyingAsset
	case domain.ScopeAccountSettlementAsset:
		overridden = domain.ScopeSettlementAsset
	case domain.ScopeBroker, domain.ScopeUnderlyingAsset, domain.ScopeSettlementAsset:
		return nil
	default:
		return fmt.Errorf(
			"scope %q not allowed for policy %q: %w",
			limit.Scope, domain.PolicyOrderSizeLimit, domain.ErrInvalid,
		)
	}
	// ruleid: limit-target-literal
	ref, ok, err := n.readOrderSizeBarrier(ctx, LimitTarget{
		Policy: domain.PolicyOrderSizeLimit, Scope: overridden, Asset: limit.Asset,
		Mode: limit.Mode,
	})
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return limit.NoLooserThan(ref)
}

// Source: framework/node/local_limits.go:539 at 9d7fe3b1.
func (n *localNode) guardTightenOnlySpotFundsPnlBounds(
	ctx context.Context,
	limit, prev domain.LimitSpotFundsPnlBounds,
	hadPrev bool,
	groupCode, currency string,
	caller domain.Caller,
) error {
	if !caller.TightenOnly() {
		return nil
	}
	if hadPrev {
		return limit.NoLooserThan(prev)
	}
	var (
		ref domain.LimitSpotFundsPnlBounds
		ok  bool
		err error
	)
	switch limit.Scope {
	case domain.ScopeGlobal:
		return nil
	case domain.ScopeAccountGroup:
		// ruleid: limit-target-literal
		ref, ok, err = n.readSpotFundsPnlBoundsBarrier(ctx, LimitTarget{
			Policy: limit.Axis.Policy(),
			Scope:  domain.ScopeGlobal,
		})
	case domain.ScopeAccount:
		ref, ok, err = n.resolvedSpotFundsPnlBounds(ctx, limit.Axis, groupCode, currency)
	default:
		return fmt.Errorf(
			"scope %q not allowed for policy %q: %w",
			limit.Scope, limit.Axis.Policy(), domain.ErrInvalid,
		)
	}
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return limit.NoLooserThan(ref)
}

// Source: framework/node/local_limits.go:614 at 9d7fe3b1.
func (n *localNode) resolvedSpotFundsPnlBounds(
	ctx context.Context, axis domain.SpotFundsPnlAxis, groupCode, currency string,
) (domain.LimitSpotFundsPnlBounds, bool, error) {
	applies := func(bound domain.LimitSpotFundsPnlBounds) bool {
		return bound.Currency == currency
	}
	if groupCode != "" {
		// ruleid: limit-target-literal
		bound, ok, err := n.readSpotFundsPnlBoundsBarrier(ctx, LimitTarget{
			Policy:       axis.Policy(),
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: groupCode,
		})
		if err != nil {
			return domain.LimitSpotFundsPnlBounds{}, false, err
		}
		if ok && applies(bound) {
			return bound, true, nil
		}
	}
	// ruleid: limit-target-literal
	bound, ok, err := n.readSpotFundsPnlBoundsBarrier(ctx, LimitTarget{
		Policy: axis.Policy(),
		Scope:  domain.ScopeGlobal,
	})
	if err != nil {
		return domain.LimitSpotFundsPnlBounds{}, false, err
	}
	if ok && applies(bound) {
		return bound, true, nil
	}
	return domain.LimitSpotFundsPnlBounds{}, false, nil
}

// Source: framework/node/local_limits.go:1013 at 9d7fe3b1.
func (n *localNode) readRateBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitRate, bool, error) {
	limits, err := n.realm.ListRateLimits(ctx, "")
	if err != nil {
		return domain.LimitRate{}, false, fmt.Errorf("read rate barrier: %w", err)
	}
	for _, limit := range limits {
		// ruleid: limit-key-axis-compare
		if limit.Scope == target.Scope && limit.Account == target.Account && limit.Asset == target.Asset {
			return limit, true, nil
		}
	}
	return domain.LimitRate{}, false, nil
}

// Source: framework/node/local_limits.go:1101 at 9d7fe3b1.
func (n *localNode) readPositionBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitPosition, bool, error) {
	limits, err := n.realm.ListPositionLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitPosition{}, false, fmt.Errorf("read position barrier: %w", err)
	}
	for _, limit := range limits {
		// ruleid: limit-key-axis-compare
		if limit.Account == target.Account && limit.Asset == target.Asset {
			return limit, true, nil
		}
	}
	return domain.LimitPosition{}, false, nil
}

// Source: framework/node/local_limits.go:1134 at 9d7fe3b1.
func (n *localNode) readOrderSizeBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitOrderSize, bool, error) {
	if target.Mode == "" {
		return domain.LimitOrderSize{}, false, fmt.Errorf("read order-size key requires mode: %w", domain.ErrInvalid)
	}
	limits, err := n.realm.ListOrderSizeLimits(ctx, "")
	if err != nil {
		return domain.LimitOrderSize{}, false, fmt.Errorf("read order-size barrier: %w", err)
	}
	for _, limit := range limits {
		// ruleid: limit-key-axis-compare
		if limit.Scope == target.Scope && limit.Account == target.Account && limit.Asset == target.Asset && limit.Mode == target.Mode {
			return limit, true, nil
		}
	}
	return domain.LimitOrderSize{}, false, nil
}

// Source: framework/node/local_limits.go:1180 at 9d7fe3b1.
func (n *localNode) readSpotFundsPnlBoundsBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitSpotFundsPnlBounds, bool, error) {
	axis, err := spotFundsPnlAxisOfPolicy(target.Policy)
	if err != nil {
		return domain.LimitSpotFundsPnlBounds{}, false, err
	}
	limits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		return domain.LimitSpotFundsPnlBounds{}, false,
			fmt.Errorf("read spot funds pnl-bounds barrier: %w", err)
	}
	for _, limit := range limits {
		// ruleid: limit-key-axis-compare
		if limit.Axis == axis &&
			limit.Scope == target.Scope &&
			limit.Account == target.Account &&
			limit.AccountGroup == target.AccountGroup {
			return limit, true, nil
		}
	}
	return domain.LimitSpotFundsPnlBounds{}, false, nil
}

// Source: framework/node/local_groups.go:671 at 9d7fe3b1.
func (n *localNode) groupProtection(
	ctx context.Context, group domain.AccountGroup,
) error {
	if group.Blocked {
		return fmt.Errorf("group %q is blocked: %w", group.Code, domain.ErrLoosening)
	}
	for _, axis := range domain.SpotFundsPnlAxes {
		// ruleid: limit-target-literal
		_, bound, err := n.readSpotFundsPnlBoundsBarrier(ctx, LimitTarget{
			Policy:       axis.Policy(),
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: group.Code,
		})
		if err != nil {
			return fmt.Errorf(
				"read spot funds pnl-bounds limits for group %q: %w", group.Code, err,
			)
		}
		if bound {
			return fmt.Errorf(
				"group %q carries a group-scoped Spot Funds P&L bound: %w",
				group.Code, domain.ErrLoosening,
			)
		}
	}
	return nil
}

// Source: framework/node/audit_detail.go:29 at 9d7fe3b1.
func setRateLimitDetail(limit domain.LimitRate) domain.AuditDetail {
	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy:  domain.PolicyRateLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	fields := append(axesFields(target),
		domain.AuditDetailField{Name: "max_orders", Value: strconv.FormatUint(limit.MaxOrders, 10)},
		domain.AuditDetailField{Name: "window", Value: limit.Window.String()},
	)
	return domain.NewAuditDetail(domain.AuditDetailSetLimit, axesParams(target), fields...)
}

// Source: framework/node/audit_detail.go:44 at 9d7fe3b1.
func setPositionLimitDetail(limit domain.LimitPosition) domain.AuditDetail {
	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy: domain.PolicyPositionLimit, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset,
	}
	fields := append(axesFields(target), domain.AuditDetailField{Name: "max_position", Value: limit.MaxPosition})
	return domain.NewAuditDetail(domain.AuditDetailSetLimit, axesParams(target), fields...)
}

// Source: framework/node/audit_detail.go:53 at 9d7fe3b1.
func setOrderSizeLimitDetail(limit domain.LimitOrderSize) domain.AuditDetail {
	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy:  domain.PolicyOrderSizeLimit,
		Mode:    limit.Mode,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	fields := axesFields(target)
	if limit.MaxQuantity != "" {
		fields = append(fields, domain.AuditDetailField{Name: "max_quantity", Value: limit.MaxQuantity})
	}
	if limit.MaxNotional != "" {
		fields = append(fields, domain.AuditDetailField{Name: "max_notional", Value: limit.MaxNotional})
	}
	return domain.NewAuditDetail(domain.AuditDetailSetLimit, axesParams(target), fields...)
}

// Source: framework/node/audit_detail.go:72 at 9d7fe3b1.
func setSpotFundsPnlBoundsLimitDetail(limit domain.LimitSpotFundsPnlBounds) domain.AuditDetail {
	// ruleid: limit-target-literal
	target := LimitTarget{
		Policy:       limit.Axis.Policy(),
		Scope:        limit.Scope,
		Account:      limit.Account,
		AccountGroup: limit.AccountGroup,
	}
	fields := append(axesFields(target),
		domain.AuditDetailField{Name: "currency", Value: limit.Currency},
	)
	if limit.LowerBound != "" {
		fields = append(fields, domain.AuditDetailField{Name: "lower_bound", Value: limit.LowerBound})
	}
	if limit.UpperBound != "" {
		fields = append(fields, domain.AuditDetailField{Name: "upper_bound", Value: limit.UpperBound})
	}
	return domain.NewAuditDetail(domain.AuditDetailSetLimit, axesParams(target), fields...)
}

// Source: framework/node/local_restore_online.go:321 at 9d7fe3b1.
func rateLimitMap(limits []domain.LimitRate) map[string]domain.LimitRate {
	out := make(map[string]domain.LimitRate, len(limits))
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		out[string(limit.Scope)+"\x00"+limit.Account.String()+"\x00"+limit.Asset] = limit
	}
	return out
}

// Source: framework/node/local_restore_online.go:329 at 9d7fe3b1.
func positionLimitMap(limits []domain.LimitPosition) map[string]string {
	out := make(map[string]string, len(limits))
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		out[limit.Account.String()+"\x00"+limit.Asset] = limit.MaxPosition
	}
	return out
}

// Source: framework/node/local_restore_online.go:337 at 9d7fe3b1.
func orderSizeLimitMap(limits []domain.LimitOrderSize) map[string]domain.LimitOrderSize {
	out := make(map[string]domain.LimitOrderSize, len(limits))
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		out[string(limit.Scope)+"\x00"+limit.Account.String()+"\x00"+limit.Asset+"\x00"+string(limit.Mode)] = limit
	}
	return out
}

// Source: framework/node/local_restore_online.go:353 at 9d7fe3b1.
func spotFundsLimitMap(
	limits []domain.LimitSpotFundsPnlBounds,
) map[string]domain.LimitSpotFundsPnlBounds {
	out := make(map[string]domain.LimitSpotFundsPnlBounds, len(limits))
	for _, limit := range limits {
		// ruleid: limit-key-string-join
		out[string(limit.Axis)+"\x00"+string(limit.Scope)+"\x00"+
			limit.Account.String()+"\x00"+limit.AccountGroup] = limit
	}
	return out
}

// Source: framework/web/httpapi/limit_dto.go:431 at 9d7fe3b1.
func sameLimitAddress(
	leftScope domain.LimitScope, leftAccount domain.AccountID, leftAsset string,
	leftMode domain.RuleMode,
	rightScope domain.LimitScope, rightAccount domain.AccountID, rightAsset string,
	rightMode domain.RuleMode,
) bool {
	// ruleid: limit-key-axis-compare
	return leftScope == rightScope && leftAccount == rightAccount && leftAsset == rightAsset && leftMode == rightMode
}

// Source: framework/web/httpapi/limit_dto.go:440 at 9d7fe3b1.
func sameSpotFundsPnlBoundsAddress(
	left domain.LimitSpotFundsPnlBounds,
	right domain.LimitSpotFundsPnlBounds,
) bool {
	// ruleid: limit-key-axis-compare
	return left.Axis == right.Axis &&
		left.Scope == right.Scope &&
		left.Account == right.Account &&
		left.AccountGroup == right.AccountGroup
}

// Source: internal/store/sqlite/backup_prune.go:273 at 9d7fe3b1.
func (rt *restoreTx) pruneLimits(
	ctx context.Context, selector backup.EntitySelector, accounts accountScope, data backup.Data,
) error {
	rateKeep := make(map[string]bool, len(data.RateLimits))
	for _, l := range data.RateLimits {
		// ruleid: limit-key-string-join
		rateKeep[limitKey(string(l.Scope), l.Account, l.Asset, "")] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_rate", rateKeep); err != nil {
		return err
	}
	sizeKeep := make(map[string]bool, len(data.OrderSizeLimits))
	for _, l := range data.OrderSizeLimits {
		// ruleid: limit-key-string-join
		sizeKeep[limitKey(string(l.Scope), l.Account, l.Asset, l.Mode)] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_order_size", sizeKeep); err != nil {
		return err
	}
	positionKeep := make(map[string]bool, len(data.PositionLimits))
	for _, l := range data.PositionLimits {
		// ruleid: limit-key-string-join
		positionKeep[limitKey(string(l.Scope), l.Account, l.Asset, "")] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_position", positionKeep); err != nil {
		return err
	}
	spotFundsPnlBounds := data.SpotFundsPnlBounds()
	spotFundsKeep := make(map[string]bool, len(spotFundsPnlBounds))
	for _, l := range spotFundsPnlBounds {
		// ruleid: limit-key-string-join
		spotFundsKeep[spotFundsPnlBoundsLimitKey(
			string(l.Axis),
			string(l.Scope),
			l.Account,
			l.AccountGroup,
		)] = true
	}
	groupScope := make(map[string]bool, len(selector.Groups)+len(data.Accounts))
	for _, group := range selector.Groups {
		groupScope[group] = true
	}
	for _, account := range data.Accounts {
		if accounts.in(account.Code) && account.GroupCode != "" {
			groupScope[account.GroupCode] = true
		}
	}
	return rt.pruneSpotFundsPnlBoundsLimits(
		ctx,
		selector,
		accounts,
		groupScope,
		spotFundsKeep,
	)
}

// Source: internal/store/sqlite/backup_prune.go:331 at 9d7fe3b1.
func (rt *restoreTx) pruneLimitTable(
	ctx context.Context, selector backup.EntitySelector, accounts accountScope,
	table string, keep map[string]bool,
) error {
	modeColumn := `''`
	if table == "limit_order_size" {
		modeColumn = `t.mode`
	}
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT t.id, t.scope, a.code, ast.code, `+modeColumn+`
		 FROM `+table+` t
		 LEFT JOIN account a   ON a.id   = t.account_id
		 LEFT JOIN asset   ast ON ast.id = t.asset_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune %s scan: %w", table, err)
	}
	type limitRow struct {
		id            int64
		scope, a, ast string
		mode          domain.RuleMode
	}
	out := make([]limitRow, 0)
	for rows.Next() {
		var (
			id         int64
			scopeStr   string
			acc, asset sql.NullString
			mode       domain.RuleMode
		)
		if err := rows.Scan(&id, &scopeStr, &acc, &asset, &mode); err != nil {
			err = errors.Join(err, rows.Close())
			return fmt.Errorf("store: prune %s row: %w", table, err)
		}
		out = append(out, limitRow{id: id, scope: scopeStr, a: acc.String, ast: asset.String, mode: mode})
	}
	if err := rows.Err(); err != nil {
		err = errors.Join(err, rows.Close())
		return fmt.Errorf("store: prune %s iterate: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close %s rows for pruning: %w", table, err)
	}
	for _, l := range out {
		// ruleid: limit-key-string-join
		if keep[limitKey(l.scope, domain.AccountID(l.a), l.ast, l.mode)] ||
			!limitAxisInScope(selector, accounts, l.a) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM `+table+` WHERE id = ?`, l.id,
		); err != nil {
			return fmt.Errorf("store: prune %s id %d: %w", table, l.id, err)
		}
	}
	return nil
}

// Source: internal/store/sqlite/backup_prune.go:389 at 9d7fe3b1.
func (rt *restoreTx) pruneSpotFundsPnlBoundsLimits(
	ctx context.Context,
	selector backup.EntitySelector,
	accounts accountScope,
	groupScope map[string]bool,
	keep map[string]bool,
) error {
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT t.id, t.axis, t.scope, a.code, g.code
		 FROM limit_spot_funds_pnl_bound t
		 LEFT JOIN account       a  ON a.id  = t.account_id
		 LEFT JOIN account_group g  ON g.id  = t.account_group_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune limit_spot_funds_pnl_bound scan: %w", err)
	}
	type limitRow struct {
		id                          int64
		axis, scope, account, group string
	}
	out := make([]limitRow, 0)
	for rows.Next() {
		var (
			id             int64
			axis, scopeStr string
			account, group sql.NullString
		)
		if err := rows.Scan(&id, &axis, &scopeStr, &account, &group); err != nil {
			err = errors.Join(err, rows.Close())
			return fmt.Errorf("store: prune limit_spot_funds_pnl_bound row: %w", err)
		}
		out = append(out, limitRow{
			id:      id,
			axis:    axis,
			scope:   scopeStr,
			account: account.String,
			group:   group.String,
		})
	}
	if err := rows.Err(); err != nil {
		err = errors.Join(err, rows.Close())
		return fmt.Errorf("store: prune limit_spot_funds_pnl_bound iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close spot funds pnl bounds rows for pruning: %w", err)
	}
	for _, l := range out {
		// ruleid: limit-key-string-join
		if keep[spotFundsPnlBoundsLimitKey(
			l.axis,
			l.scope,
			domain.AccountID(l.account),
			l.group,
		)] || !spotFundsPnlBoundsAxisInScope(
			selector,
			accounts,
			groupScope,
			l.account,
			l.group,
		) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM limit_spot_funds_pnl_bound WHERE id = ?`, l.id,
		); err != nil {
			return fmt.Errorf(
				"store: prune limit_spot_funds_pnl_bound id %d: %w", l.id, err,
			)
		}
	}
	return nil
}

// Source: internal/store/sqlite/backup_prune.go:999 at 9d7fe3b1.
func limitKey(scope string, account domain.AccountID, asset string, mode domain.RuleMode) string {
	// ruleid: limit-key-string-join
	return scope + "\x00" + account.String() + "\x00" + asset + "\x00" + string(mode)
}

// Source: internal/store/sqlite/backup_prune.go:1003 at 9d7fe3b1.
func spotFundsPnlBoundsLimitKey(
	axis, scope string,
	account domain.AccountID,
	group string,
) string {
	// ruleid: limit-key-string-join
	return axis + "\x00" + scope + "\x00" + account.String() + "\x00" + group
}

// Source: internal/store/sqlite/limits.go:447 at 9d7fe3b1.
func (r *realmStore) DeleteRateLimit(
	ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, db, account, asset)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		// ruleid: limit-key-sql
		`DELETE FROM limit_rate
		 WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		scope, accountID, assetID,
	)
	if err != nil {
		return fmt.Errorf("store: delete rate limit: %w", err)
	}
	return notFoundIfNoRows(res, "rate limit", string(scope))
}

// Source: internal/store/sqlite/limits.go:574 at 9d7fe3b1.
func (r *realmStore) DeleteOrderSizeLimit(
	ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
	mode domain.RuleMode,
) error {
	if err := domain.ValidateOrderSizeMode(mode); err != nil {
		return fmt.Errorf("store: delete order size limit: %w", err)
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, db, account, asset)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		// ruleid: limit-key-sql
		`DELETE FROM limit_order_size
		 WHERE scope = ? AND account_id IS ? AND asset_id IS ? AND mode = ?`,
		scope, accountID, assetID, mode,
	)
	if err != nil {
		return fmt.Errorf("store: delete order size limit: %w", err)
	}
	return notFoundIfNoRows(res, "order size limit", string(scope))
}

// Source: internal/store/sqlite/limits.go:690 at 9d7fe3b1.
func (r *realmStore) DeletePositionLimit(ctx context.Context, account domain.AccountID, asset string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, db, account, asset)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx,
		// ruleid: limit-key-sql
		`DELETE FROM limit_position WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		domain.ScopeAccountAsset, accountID, assetID,
	)
	if err != nil {
		return fmt.Errorf("store: delete position limit: %w", err)
	}
	return notFoundIfNoRows(res, "position limit", account.String()+":"+asset)
}

// Source: internal/store/sqlite/limits.go:799 at 9d7fe3b1.
func (r *realmStore) DeleteSpotFundsPnlBoundsLimit(
	ctx context.Context,
	axis domain.SpotFundsPnlAxis,
	scope domain.LimitScope,
	account domain.AccountID,
	accountGroup string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, groupID, err := resolveSubjectIDs(ctx, db, account, accountGroup)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		// ruleid: limit-key-sql
		`DELETE FROM limit_spot_funds_pnl_bound
		 WHERE axis = ?
		   AND scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?`,
		axis, scope, accountID, groupID,
	)
	if err != nil {
		return fmt.Errorf("store: delete spot funds pnl bounds limit: %w", err)
	}
	return notFoundIfNoRows(res, "spot funds pnl bounds limit", string(scope))
}

// Source: internal/store/sqlite/limits.go:864 at 9d7fe3b1.
func limitKeyWhere(
	scope domain.LimitScope,
	accountID, assetID sql.NullInt64,
	mode *domain.RuleMode,
) (string, []any) {
	// ruleid: limit-key-sql
	where := `scope = ? AND account_id IS ? AND asset_id IS ?`
	args := []any{scope, accountID, assetID}
	if mode != nil {
		where += ` AND mode = ?`
		args = append(args, *mode)
	}
	return where, args
}

// Source: internal/store/sqlite/limits.go:945 at 9d7fe3b1.
func putSpotFundsPnlBoundsRow(
	ctx context.Context,
	db *sql.DB,
	axis domain.SpotFundsPnlAxis,
	scope domain.LimitScope,
	accountID, groupID sql.NullInt64,
	insertFn func(context.Context, sqlExecer) error,
) (err error) {
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return fmt.Errorf("store: begin put spot funds pnl bounds limit: %w", err)
	}
	defer rollbackTransaction(ctx, &err, tx)

	if _, err := tx.ExecContext(
		ctx,
		// ruleid: limit-key-sql
		`DELETE FROM limit_spot_funds_pnl_bound
		 WHERE axis = ?
		   AND scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?`,
		axis, scope, accountID, groupID,
	); err != nil {
		return fmt.Errorf("store: delete old spot funds pnl bounds limit: %w", err)
	}
	if err := insertFn(ctx, tx); err != nil {
		return fmt.Errorf("store: insert spot funds pnl bounds limit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit put spot funds pnl bounds limit: %w", err)
	}
	return nil
}

// Source: internal/store/sqlite/backup_restore.go:608 at 9d7fe3b1.
func (rt *restoreTx) putSpotFundsPnlBoundsLimit(
	ctx context.Context,
	axis domain.SpotFundsPnlAxis,
	scope domain.LimitScope,
	accountID, groupID sql.NullInt64,
	insertSQL string,
	insertArgs []any,
) (int, error) {
	exists, err := rowExists(
		ctx, rt.tx,
		// ruleid: limit-key-sql
		`SELECT 1 FROM limit_spot_funds_pnl_bound
		 WHERE axis = ?
		   AND scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?`,
		axis, scope, accountID, groupID,
	)
	if err != nil {
		return 0, err
	}
	if rt.skip(backup.SectionRiskLimits, exists) {
		return 0, nil
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		// ruleid: limit-key-sql
		`DELETE FROM limit_spot_funds_pnl_bound
		 WHERE axis = ?
		   AND scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?`,
		axis, scope, accountID, groupID,
	); err != nil {
		return 0, fmt.Errorf("store: restore delete limit_spot_funds_pnl_bound: %w", err)
	}
	if _, err := rt.tx.ExecContext(ctx, insertSQL, insertArgs...); err != nil {
		return 0, fmt.Errorf("store: restore insert limit_spot_funds_pnl_bound: %w", err)
	}
	return 1, nil
}

// Additional spellings of the required shapes.
func directKeyLiterals() {
	// ruleid: limit-key-literal
	_ = LimitKey{}
	// ruleid: limit-key-literal
	_ = domain.LimitKey{}
	// ruleid: limit-key-literal
	_ = domain.LimitKey{policy: policy}
	// ruleid: limit-target-literal
	_ = node.LimitTarget{Policy: policy}
}

func alternateJoins(limit domain.LimitRate, scope, account, asset string) {
	// ruleid: limit-key-string-join
	_ = fmt.Sprintf("%s\x00%s", limit.Account, limit.Asset)
	// ruleid: limit-key-string-join
	_ = strings.Join([]string{string(limit.Scope), limit.Account.String(), limit.Asset}, "\x00")
	// ruleid: limit-key-string-join
	_ = fmt.Sprintf("%s:%s", scope, account)
	// ruleid: limit-key-string-join
	_ = strings.Join([]string{scope, account, asset}, ":")
	// ok: limit-key-string-join
	_ = fmt.Errorf("scope %s account %s: %w", scope, account, err)
	// ok: limit-key-string-join
	log.Printf("scope %s account %s", scope, account)
}

// Negative source: framework/domain/trading.go:123.
func (a TradingAccess) SameSubject(other TradingAccess) bool {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	return a.Account == other.Account && a.AccountGroup == other.AccountGroup
}

// Negative source: internal/store/sqlite/trading.go:372.
func (r *realmStore) RemoveTradingAccess(
	ctx context.Context, connection domain.ExternalID,
	account domain.AccountID, accountGroup string,
) error {
	if err := domain.ValidateTradingAccess(domain.TradingAccess{
		Account: account, AccountGroup: accountGroup, Connection: connection,
	}); err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	// An unknown subject holds no grant, so it reads as an absent grant.
	accountID, groupID, err := resolveSubjectIDs(ctx, db, account, accountGroup)
	if errors.Is(err, domain.ErrInvalid) {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("trading access %q: %w", connection.String(), domain.ErrNotFound)
	}
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		`DELETE FROM trading_access
		 WHERE connection_id = (
		   SELECT id FROM trading_connection WHERE external_id = ?
		 )
		 AND account_id IS ?
		 AND account_group_id IS ?`,
		connection.Bytes(), accountID, groupID,
	)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: remove trading access: %w", err)
	}
	return notFoundIfNoRows(res, "trading access", connection.String())
}

// Negative source: internal/store/sqlite/backup_prune.go:231.
func (rt *restoreTx) prunePositions(
	ctx context.Context, positions accountScope, balances []backup.Balance,
) error {
	keep := make(map[string]bool, len(balances))
	for _, b := range balances {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		keep[b.Account.String()+"\x00"+b.Asset] = true
	}
	rows, err := rt.tx.QueryContext(
		ctx,
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		`SELECT a.code, ast.code
		 FROM balance b
		 JOIN account a   ON a.id   = b.account_id
		 JOIN asset   ast ON ast.id = b.asset_id`,
	)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: prune positions scan: %w", err)
	}
	pairs, err := scanCodePairs(rows)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		if keep[p.a+"\x00"+p.b] || !positions.in(p.a) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			`DELETE FROM balance
			 WHERE account_id = (SELECT id FROM account WHERE code = ?)
			   AND asset_id   = (SELECT id FROM asset   WHERE code = ?)`,
			p.a, p.b,
		); err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune balance %q/%q: %w", p.a, p.b, err)
		}
	}
	return nil
}

// Negative source: internal/store/sqlite/backup_prune.go:522.
func (rt *restoreTx) pruneMarketData(ctx context.Context, data backup.Data) error {
	keepInstr := make(map[string]bool, len(data.MarketDataInstruments))
	for _, instr := range data.MarketDataInstruments {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		keepInstr[instr.Instance.String()+"\x00"+instr.ExternalSymbol] = true
	}
	instrRows, err := rt.tx.QueryContext(
		ctx,
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		`SELECT mdi.id, i.external_id, mdi.external_symbol
		 FROM market_data_instrument mdi
		 JOIN market_data_instance i ON i.id = mdi.instance_id`,
	)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: prune md instruments scan: %w", err)
	}
	type instrRow struct {
		id  int64
		key string
	}
	instrs := make([]instrRow, 0)
	for instrRows.Next() {
		var (
			id     int64
			extID  []byte
			symbol string
		)
		if err := instrRows.Scan(&id, &extID, &symbol); err != nil {
			err = errors.Join(err, instrRows.Close())
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune md instrument row: %w", err)
		}
		xid, err := domain.ExternalIDFromBytes(extID)
		if err != nil {
			err = errors.Join(err, instrRows.Close())
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune md instrument external id: %v", err)
		}
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		instrs = append(instrs, instrRow{id: id, key: xid.String() + "\x00" + symbol})
	}
	if err := instrRows.Err(); err != nil {
		err = errors.Join(err, instrRows.Close())
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: prune md instruments iterate: %w", err)
	}
	if err := instrRows.Close(); err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: close md instruments for pruning: %w", err)
	}
	for _, instr := range instrs {
		if keepInstr[instr.key] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			ctx, `DELETE FROM market_data_instrument WHERE id = ?`, instr.id,
		); err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune md instrument id %d: %w", instr.id, err)
		}
	}

	keepInst := make(map[string]bool, len(data.MarketDataInstances))
	for _, inst := range data.MarketDataInstances {
		keepInst[inst.ExternalID.String()] = true
	}
	instRows, err := rt.tx.QueryContext(
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		ctx, `SELECT id, external_id FROM market_data_instance`,
	)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: prune md instances scan: %w", err)
	}
	type instRow struct {
		id  int64
		key string
	}
	insts := make([]instRow, 0)
	for instRows.Next() {
		var (
			id    int64
			extID []byte
		)
		if err := instRows.Scan(&id, &extID); err != nil {
			err = errors.Join(err, instRows.Close())
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune md instance row: %w", err)
		}
		xid, err := domain.ExternalIDFromBytes(extID)
		if err != nil {
			err = errors.Join(err, instRows.Close())
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune md instance external id: %v", err)
		}
		insts = append(insts, instRow{id: id, key: xid.String()})
	}
	if err := instRows.Err(); err != nil {
		err = errors.Join(err, instRows.Close())
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: prune md instances iterate: %w", err)
	}
	if err := instRows.Close(); err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: close md instances for pruning: %w", err)
	}
	for _, inst := range insts {
		if keepInst[inst.key] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			ctx, `DELETE FROM market_data_instance WHERE id = ?`, inst.id,
		); err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune md instance id %d: %w", inst.id, err)
		}
	}
	return nil
}

// Negative source: internal/store/sqlite/backup_prune.go:789.
func (rt *restoreTx) pruneExecutionReportLinks(
	ctx context.Context,
	accounts accountScope,
	links []backup.ExecutionReportEventLink,
) error {
	keep := make(map[string]bool, len(links))
	for _, link := range links {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		keep[link.Report.String()+"\x00"+link.Event.String()] = true
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	rows, err := rt.tx.QueryContext(ctx, `
		SELECT er.external_id, oe.external_id, ere.event_id, a.code
		FROM execution_report_event ere
		JOIN execution_report er ON er.id = ere.report_id
		JOIN order_event oe ON oe.id = ere.event_id
		JOIN order_record o ON o.id = er.order_id
		JOIN account a ON a.id = o.account_id`)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: prune execution report links scan: %w", err)
	}
	type linkRow struct {
		reportID   []byte
		eventID    []byte
		eventRowID int64
		account    string
	}
	var stored []linkRow
	for rows.Next() {
		var row linkRow
		if err := rows.Scan(
			&row.reportID, &row.eventID, &row.eventRowID, &row.account,
		); err != nil {
			err = errors.Join(err, rows.Close())
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune execution report link row: %w", err)
		}
		row.reportID = append([]byte(nil), row.reportID...)
		row.eventID = append([]byte(nil), row.eventID...)
		stored = append(stored, row)
	}
	if err := rows.Err(); err != nil {
		err = errors.Join(err, rows.Close())
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: prune execution report links iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fmt.Errorf("store: close execution report links for pruning: %w", err)
	}
	for _, row := range stored {
		reportID, err := domain.ExternalIDFromBytes(row.reportID)
		if err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune linked execution report id: %v", err)
		}
		eventID, err := domain.ExternalIDFromBytes(row.eventID)
		if err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf("store: prune linked execution event id: %v", err)
		}
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		if keep[reportID.String()+"\x00"+eventID.String()] || !accounts.in(row.account) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			`DELETE FROM execution_report_event WHERE event_id = ?`,
			row.eventRowID,
		); err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf(
				"store: prune execution report %q event %q: %w",
				reportID,
				eventID,
				err,
			)
		}
	}
	return nil
}

// Negative source: framework/node/local_restore_online.go:398.
func restoreMarketDataKey(instance domain.ExternalID, external string) string {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	return instance.String() + "\x00" + external
}

// Negative source: framework/node/local_restore_online.go:402.
func restoreBalanceKey(account domain.AccountID, asset string) string {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	return account.String() + "\x00" + asset
}

// Negative source: engine/mapping.go:974-999; current tree.
func orderSizeLimitValues(
	limits []domain.LimitOrderSize, res idResolver,
) (map[orderSizeLimitKey]orderSizeLimitValue, error) {
	values := make(map[orderSizeLimitKey]orderSizeLimitValue, len(limits))
	for _, limit := range limits {
		// ok: limit-key-composite, limit-key-string-join
		key := orderSizeLimitKey{scope: limit.Scope}
		if limit.Account != "" {
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, err
			}
			key.account = account
		}
		if limit.Asset != "" {
			asset, err := res.asset(limit.Asset)
			if err != nil {
				return nil, err
			}
			key.asset = asset.Safe()
		}
		// ok: limit-key-composite, limit-key-string-join
		values[key] = orderSizeLimitValue{
			maxQuantity: limit.MaxQuantity, maxNotional: limit.MaxNotional,
		}
	}
	return values, nil
}

// Negative source: engine/mapping.go:689-776; current tree.
func orderSizeAxes(limits []domain.LimitOrderSize, res idResolver) (
	*policies.OrderSizeBrokerBarrier,
	[]policies.OrderSizeAssetBarrier,
	[]policies.OrderSizeAccountAssetBarrier,
	error,
) {
	var broker *policies.OrderSizeBrokerBarrier
	assets := []policies.OrderSizeAssetBarrier{}
	assetIndexes := make(map[string]int)
	accountAssets := []policies.OrderSizeAccountAssetBarrier{}
	accountAssetIndexes := make(map[accountAssetKey]int)

	for _, limit := range limits {
		if err := limit.Validate(); err != nil {
			return nil, nil, nil, fmt.Errorf(
				"engine: invalid order_size_limit: %w", err,
			)
		}
		size, err := orderSizeValue(limit)
		if err != nil {
			return nil, nil, nil, err
		}
		switch limit.Scope {
		case domain.ScopeBroker:
			// The axis holds one barrier; a second one would silently replace the
			// first and lose a configured limit.
			if broker != nil {
				return nil, nil, nil, fmt.Errorf(
					"engine: order_size_limit carries more than one broker barrier")
			}
			broker = &policies.OrderSizeBrokerBarrier{Limit: size}
		case domain.ScopeUnderlyingAsset, domain.ScopeSettlementAsset:
			asset, err := res.asset(limit.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			assetKey := asset.Safe()
			if index, ok := assetIndexes[assetKey]; ok {
				merged, err := mergeOrderSizeValue(
					assets[index].Limit, size, fmt.Sprintf("asset %q", limit.Asset),
				)
				if err != nil {
					return nil, nil, nil, err
				}
				assets[index].Limit = merged
				break
			}
			assetIndexes[assetKey] = len(assets)
			assets = append(assets, policies.OrderSizeAssetBarrier{
				Limit: size,
				Asset: asset,
			})
		case domain.ScopeAccountUnderlyingAsset,
			domain.ScopeAccountSettlementAsset:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, nil, nil, err
			}
			asset, err := res.asset(limit.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			// ok: limit-key-composite, limit-key-string-join
			key := accountAssetKey{account: account, asset: asset.Safe()}
			// ok: limit-key-composite, limit-key-string-join
			if index, ok := accountAssetIndexes[key]; ok {
				merged, err := mergeOrderSizeValue(
					accountAssets[index].Limit,
					size,
					fmt.Sprintf("account %q asset %q", limit.Account, limit.Asset),
				)
				if err != nil {
					return nil, nil, nil, err
				}
				accountAssets[index].Limit = merged
				break
			}
			// ok: limit-key-composite, limit-key-string-join
			accountAssetIndexes[key] = len(accountAssets)
			accountAssets = append(accountAssets, policies.OrderSizeAccountAssetBarrier{
				Limit:     size,
				AccountID: account,
				Asset:     asset,
			})
		default:
			return nil, nil, nil, fmt.Errorf(
				"engine: order_size_limit unsupported scope %q", limit.Scope)
		}
	}
	return broker, assets, accountAssets, nil
}

// Negative source: internal/store/sqlite/backup_restore.go:1143.
func (rt *restoreTx) restoreTradingAccess(
	ctx context.Context, accesses []backup.TradingAccess,
) error {
	for _, archived := range accesses {
		access := domain.TradingAccess{
			Account:      archived.Account,
			AccountGroup: archived.AccountGroup,
			Connection:   archived.Connection,
			VenueAccount: archived.VenueAccount,
		}
		if err := restoreValidationError(domain.ValidateTradingAccess(access)); err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf(
				"store: restore trading access %q/%q: %w",
				access.SubjectLabel(), access.Connection, err,
			)
		}
		accountID, groupID, err := resolveSubjectIDs(
			ctx, rt.tx, access.Account, access.AccountGroup,
		)
		if err != nil {
			if !errors.Is(err, domain.ErrInvalid) {
				return err
			}
			if access.AccountGroup != "" {
				// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
				return fmt.Errorf(
					"store: restore trading access %q/%q account group is "+
						"missing; include that group in the %q section: %w",
					access.AccountGroup, access.Connection,
					backup.SectionAccountsGroups, domain.ErrInvalid,
				)
			}
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf(
				"store: restore trading access %q/%q account is missing; "+
					"include that account in the %q section: %w",
				access.Account, access.Connection,
				backup.SectionAccountsGroups, domain.ErrInvalid,
			)
		}
		connectionID, err := rt.restoreTradingConnectionID(
			ctx, access.Connection, "trading access",
		)
		if err != nil {
			return err
		}
		exists, err := rowExists(
			ctx, rt.tx,
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			`SELECT 1 FROM trading_access
			 WHERE connection_id = ? AND account_id IS ? AND account_group_id IS ?`,
			connectionID, accountID, groupID,
		)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionTrading, exists) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			`INSERT INTO trading_access
			 (account_id, account_group_id, connection_id, venue_account)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT (
			   connection_id, COALESCE(account_id, 0), COALESCE(account_group_id, 0)
			 ) DO UPDATE SET venue_account = excluded.venue_account`,
			accountID, groupID, connectionID, access.VenueAccount,
		); err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf(
				"store: restore trading access %q/%q: %w",
				access.SubjectLabel(), access.Connection, err,
			)
		}
		if access.AccountGroup != "" {
			accounts, err := tradingGroupAccounts(ctx, rt.tx, groupID)
			if err != nil {
				return err
			}
			for _, account := range accounts {
				rt.isolationAccounts[account] = true
			}
		} else {
			rt.isolationAccounts[access.Account] = true
		}
		rt.applyRuntime(backup.SectionTrading, 1)
	}
	return nil
}

// Negative source: framework/backend/backend_limits.go:70.
func (s *Service) ListLimits(
	ctx context.Context, account domain.AccountID,
) (node.AccountLimits, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return node.AccountLimits{}, err
	}
	if account != "" {
		if account, err = s.scope.Account(caller, account); err != nil {
			return node.AccountLimits{}, err
		}
	}
	groupVisible, err := s.visibleGroups(ctx, caller)
	if err != nil {
		return node.AccountLimits{}, err
	}
	limits, err := s.node.ListLimits(ctx, account)
	if err != nil {
		return node.AccountLimits{}, err
	}
	hidden := func(owner domain.AccountID, group string) bool {
		return s.scope.Visible(caller, owner) != nil ||
			(group != "" && !groupVisible(group))
	}
	limits.RateLimits = slices.DeleteFunc(
		slices.Clone(limits.RateLimits),
		func(limit domain.LimitRate) bool { return hidden(limit.Account, "") },
	)
	limits.OrderSizeLimits = slices.DeleteFunc(
		slices.Clone(limits.OrderSizeLimits),
		func(limit domain.LimitOrderSize) bool { return hidden(limit.Account, "") },
	)
	limits.PositionLimits = slices.DeleteFunc(
		slices.Clone(limits.PositionLimits),
		func(limit domain.LimitPosition) bool { return hidden(limit.Account, "") },
	)
	limits.SpotFundsPnlBoundsLimits = slices.DeleteFunc(
		slices.Clone(limits.SpotFundsPnlBoundsLimits),
		func(limit domain.LimitSpotFundsPnlBounds) bool {
			return hidden(limit.Account, limit.AccountGroup)
		},
	)
	return limits, nil
}

// Negative source: framework/node/local_groups.go:777.
func isGroupSpotFundsPnlBoundsLimit(
	limit domain.LimitSpotFundsPnlBounds, group string,
) bool {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	return limit.Scope == domain.ScopeAccountGroup && limit.AccountGroup == group
}

// Negative source: engine/openpit.go:1411.
func configureStoredSpotFundsPnlAxes(
	configurator configure.Configurator,
	res idResolver,
	limits []domain.LimitSpotFundsPnlBounds,
) ([]domain.AccountBlock, error) {
	var blocks []domain.AccountBlock
	for _, axis := range domain.SpotFundsPnlAxes {
		if !slices.ContainsFunc(
			limits,
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			func(limit domain.LimitSpotFundsPnlBounds) bool { return limit.Axis == axis },
		) {
			continue
		}
		configuration, err := configureSpotFundsPnlBounds(configurator, res, axis, limits)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, configuration.AccountBlocks...)
	}
	return blocks, nil
}

// Negative source: internal/store/sqlite/limits.go:174.
func policyListWhere(filter fwstore.PolicyListFilter) (string, []any, error) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	if filter.Restriction.Selection != nil {
		appendPolicySelection(&clauses, &args, filter.Restriction)
	} else {
		appendAccountRestriction(
			&clauses, &args, "account_code", "account_code IS NULL", filter.Restriction,
		)
		// An account-group barrier is kept only when the restricted account is a
		// member of its group. The domain forbids a group on an account-axis
		// barrier, so this narrows the unowned rows alone.
		if filter.Restriction.Restricted() {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			clauses = append(clauses, `(account_group_code IS NULL OR account_group_code IN (
			SELECT g.code FROM account m JOIN account_group g ON g.id = m.group_id
			WHERE m.code = ?))`)
			args = append(args, filter.Restriction.Account.String())
		}
	}
	appendMatcher(&clauses, &args, "account_code", filter.Account)
	appendMatcher(&clauses, &args, "account_group_code", filter.AccountGroup)
	appendMatcher(&clauses, &args, "COALESCE(asset_code, currency_code)", filter.Asset)
	if filter.Scope != "" {
		switch filter.Scope {
		case domain.ScopeBroker, domain.ScopeGlobal, domain.ScopeAsset,
			domain.ScopeAccount, domain.ScopeAccountGroup, domain.ScopeAccountAsset,
			domain.ScopeUnderlyingAsset, domain.ScopeSettlementAsset,
			domain.ScopeAccountUnderlyingAsset,
			domain.ScopeAccountSettlementAsset:
		default:
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return "", nil, fmt.Errorf(
				"invalid policy scope %q: %w", filter.Scope, domain.ErrInvalid,
			)
		}
		clauses = append(clauses, "scope = ?")
		args = append(args, filter.Scope)
	}
	if filter.Kind != nil {
		clauses = append(clauses, "kind = ?")
		args = append(args, string(*filter.Kind))
	}
	if filter.Mode != "" {
		clauses = append(clauses, "kind = 'order_size_limit' AND mode = ?")
		args = append(args, filter.Mode)
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

// Negative source: internal/store/sqlite/limits.go:225.
func policyListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		"account":      "account_code",
		"accountGroup": "account_group_code",
		"asset":        "COALESCE(asset_code, currency_code)",
		"lowerBound":   "lower_bound COLLATE DECIMAL",
		"maxNotional":  "max_notional COLLATE DECIMAL",
		"maxOrders":    "max_orders",
		"maxQuantity":  "max_quantity COLLATE DECIMAL",
		"maxPosition":  "max_position COLLATE DECIMAL",
		"policy":       "kind",
		"scope":        "scope",
		"upperBound":   "upper_bound COLLATE DECIMAL",
	}
	column := columns[sort.Column]
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	if column == "" {
		column = "kind"
	}
	direction := "ASC"
	tieDirection := "ASC"
	if sort.Descending {
		direction = "DESC"
		tieDirection = "DESC"
	}
	// The (kind, scope, account, asset, mode) composite is unique across the union, so
	// it is a deterministic tiebreak that does not depend on the per-table
	// surrogate id. stable_id stays the final tiebreak for the degenerate case
	// where the composite repeats.
	tie := "kind " + tieDirection + ", scope " + tieDirection +
		", account_code " + tieDirection +
		", account_group_code " + tieDirection +
		", COALESCE(asset_code, currency_code) " + tieDirection +
		", mode " + tieDirection +
		", stable_id " + tieDirection
	return " ORDER BY " + column + " " + direction + ", " + tie
}

// Negative source: internal/store/sqlite/limits.go:367.
func (r *realmStore) ListRateLimits(
	ctx context.Context, account domain.AccountID,
) (value []domain.LimitRate, resultErr error) {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q := `
SELECT lr.scope, a.code, ast.code, lr.max_orders, lr.window
FROM limit_rate lr
LEFT JOIN account a   ON a.id   = lr.account_id
LEFT JOIN asset   ast ON ast.id = lr.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q += ` ORDER BY lr.scope, a.code, ast.code`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf("store: list rate limits: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()

	result := make([]domain.LimitRate, 0)
	for rows.Next() {
		l, err := scanRateLimit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf("store: iterate rate limits: %w", err)
	}
	return result, nil
}

// Negative source: internal/store/sqlite/limits.go:510.
func (r *realmStore) ListOrderSizeLimits(
	ctx context.Context, account domain.AccountID,
) (value []domain.LimitOrderSize, resultErr error) {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q := `
SELECT los.scope, a.code, ast.code, los.max_quantity, los.max_notional, los.mode
FROM limit_order_size los
LEFT JOIN account a   ON a.id   = los.account_id
LEFT JOIN asset   ast ON ast.id = los.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q += ` ORDER BY los.scope, a.code, ast.code, los.mode`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf("store: list order size limits: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()

	result := make([]domain.LimitOrderSize, 0)
	for rows.Next() {
		l, err := scanOrderSizeLimit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf("store: iterate order size limits: %w", err)
	}
	return result, nil
}

// Negative source: internal/store/sqlite/limits.go:613.
func (r *realmStore) ListPositionLimits(
	ctx context.Context, account domain.AccountID,
) (value []domain.LimitPosition, resultErr error) {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q := `SELECT lp.scope, a.code, ast.code, lp.max_position
FROM limit_position lp
JOIN account a ON a.id = lp.account_id
JOIN asset ast ON ast.id = lp.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q += ` ORDER BY a.code, ast.code`
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf("store: list position limits: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	result := make([]domain.LimitPosition, 0)
	for rows.Next() {
		var limit domain.LimitPosition
		if err := rows.Scan(&limit.Scope, &limit.Account, &limit.Asset, &limit.MaxPosition); err != nil {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return nil, fmt.Errorf("store: scan position limit: %w", err)
		}
		result = append(result, limit)
	}
	if err := rows.Err(); err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf("store: iterate position limits: %w", err)
	}
	return result, nil
}

// Negative source: internal/store/sqlite/limits.go:690.
func (r *realmStore) ListSpotFundsPnlBoundsLimits(
	ctx context.Context, account domain.AccountID,
) (value []domain.LimitSpotFundsPnlBounds, resultErr error) {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q := `
	SELECT lsfpb.axis, lsfpb.scope, a.code, g.code, c.code,
	       lsfpb.lower_bound, lsfpb.upper_bound
FROM limit_spot_funds_pnl_bound lsfpb
LEFT JOIN account       a  ON a.id  = lsfpb.account_id
LEFT JOIN account_group g  ON g.id  = lsfpb.account_group_id
JOIN asset               c  ON c.id  = lsfpb.currency_asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	q += ` ORDER BY lsfpb.axis, lsfpb.scope, a.code, g.code`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf("store: list spot funds pnl bounds limits: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()

	result := make([]domain.LimitSpotFundsPnlBounds, 0)
	for rows.Next() {
		l, err := scanSpotFundsPnlBoundsLimit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return nil, fmt.Errorf(
			"store: iterate spot funds pnl bounds limits: %w", err,
		)
	}
	return result, nil
}

// Negative source: internal/store/sqlite/limits.go:822.
func limitKeyWhere(key domain.LimitKey, ids limitKeyIDs) (limitKeyPredicate, error) {
	predicate := limitKeyPredicate{subject: string(key.Scope())}
	switch key.Policy() {
	case domain.PolicyRateLimit:
		predicate.table, predicate.noun = "limit_rate", "rate limit"
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		predicate.where = `scope = ? AND account_id IS ? AND asset_id IS ?`
		predicate.args = []any{key.Scope(), ids.account, ids.asset}
	case domain.PolicyOrderSizeLimit:
		predicate.table, predicate.noun = "limit_order_size", "order size limit"
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		predicate.where = `scope = ? AND account_id IS ? AND asset_id IS ? AND mode = ?`
		predicate.args = []any{key.Scope(), ids.account, ids.asset, key.Mode()}
	case domain.PolicyPositionLimit:
		predicate.table, predicate.noun = "limit_position", "position limit"
		predicate.subject = key.Account().String() + ":" + key.Asset()
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		predicate.where = `scope = ? AND account_id IS ? AND asset_id IS ?`
		predicate.args = []any{key.Scope(), ids.account, ids.asset}
	case domain.PolicySpotFundsPnlBoundsKillSwitch,
		domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
		domain.PolicySpotFundsTotalPnlBoundsKillSwitch:
		axis, ok := domain.SpotFundsPnlAxisOf(key.Policy())
		if !ok {
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return limitKeyPredicate{}, fmt.Errorf("store: policy %q has no spot funds pnl axis: %w", key.Policy(), domain.ErrInvalid)
		}
		predicate.table, predicate.noun = "limit_spot_funds_pnl_bound", "spot funds pnl bounds limit"
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		predicate.where = `axis = ? AND scope = ? AND account_id IS ? AND account_group_id IS ?`
		predicate.args = []any{axis, key.Scope(), ids.account, ids.group}
	default:
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return limitKeyPredicate{}, fmt.Errorf("store: policy %q has no limit barrier table: %w", key.Policy(), domain.ErrInvalid)
	}
	return predicate, nil
}

// Negative source: framework/web/httpapi/limit_dto.go:356.
func persistedLimit[Limit domain.KeyedLimit](
	ctx context.Context, svc LimitReader,
	target Limit,
	slice func(node.AccountLimits) []Limit,
) (Limit, error) {
	var zero Limit
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	key, err := target.Key()
	if err != nil {
		return zero, err
	}
	limits, err := svc.ListLimits(ctx, key.Account())
	if err != nil {
		return zero, err
	}
	for _, limit := range slice(limits) {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		limitKey, err := domain.StoredLimitKey(limit)
		if err != nil {
			return zero, err
		}
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		if limitKey == key {
			return limit, nil
		}
	}
	return zero, domain.ErrNotFound
}

// Negative source: framework/node/local_restore_online.go:343.
// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
func limitMap[Limit domain.KeyedLimit](limits []Limit) (map[domain.LimitKey]Limit, error) {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	out := make(map[domain.LimitKey]Limit, len(limits))
	for _, limit := range limits {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		key, err := domain.StoredLimitKey(limit)
		if err != nil {
			return nil, err
		}
		out[key] = limit
	}
	return out, nil
}

// Negative source: httpapi/httpapi_limits.go:236.
func handleDeleteLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		target := node.LimitTarget{
			Policy:       q.Get("policy"),
			Mode:         domain.RuleMode(q.Get("mode")),
			Scope:        domain.LimitScope(q.Get("scope")),
			Account:      domain.AccountID(q.Get("account")),
			AccountGroup: q.Get("accountGroup"),
			Asset:        q.Get("asset"),
		}
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		if target.Policy == domain.PolicyOrderSizeLimit && target.Mode == "" {
			target.Mode = domain.RuleModeReject
		}
		if err := svc.DeleteLimit(r.Context(), target); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// Negative source: framework/backup/backup.go:1291.
func (d *Data) SetSpotFundsPnlBounds(limits []domain.LimitSpotFundsPnlBounds) error {
	var realized, unrealized, total []domain.LimitSpotFundsPnlBounds
	for _, limit := range limits {
		axis := limit.Axis
		limit.Axis = ""
		switch axis {
		case domain.SpotFundsPnlAxisRealized:
			realized = append(realized, limit)
		case domain.SpotFundsPnlAxisUnrealized:
			unrealized = append(unrealized, limit)
		case domain.SpotFundsPnlAxisTotal:
			total = append(total, limit)
		default:
			// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
			return fmt.Errorf(
				"backup: unknown spot funds pnl axis %q: %w", axis, domain.ErrInvalid,
			)
		}
	}
	d.SpotFundsPnlBoundsLimits = realized
	d.SpotFundsUnrealizedPnlBoundsLimits = unrealized
	d.SpotFundsTotalPnlBoundsLimits = total
	return nil
}

// Negative source: engine/openpit.go:800.
func configureSpotFundsPnlBounds(
	configurator configure.Configurator,
	res idResolver,
	axis domain.SpotFundsPnlAxis,
	limits []domain.LimitSpotFundsPnlBounds,
) (fwengine.PolicyConfigurationResult, error) {
	global, groups, accounts, err := spotFundsPnlBoundsAxes(axis, limits, res)
	if err != nil {
		return fwengine.PolicyConfigurationResult{}, err
	}
	var outcomes configure.AccountBlockOutcomes
	switch axis {
	case domain.SpotFundsPnlAxisRealized:
		outcomes, err = configurator.SpotFundsPnlBoundsKillSwitch(
			policies.SpotFundsPolicyName, global, groups, accounts,
		)
	case domain.SpotFundsPnlAxisUnrealized:
		outcomes, err = configurator.SpotFundsPnlBoundsAxis(
			policies.SpotFundsPolicyName,
			policies.SpotFundsPnlBoundsAxisAccountUnrealized,
			global, groups, accounts,
		)
	case domain.SpotFundsPnlAxisTotal:
		outcomes, err = configurator.SpotFundsPnlBoundsAxis(
			policies.SpotFundsPolicyName,
			policies.SpotFundsPnlBoundsAxisAccountTotal,
			global, groups, accounts,
		)
	default:
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf(
			"engine: unknown spot funds pnl axis %q: %w", axis, domain.ErrInvalid,
		)
	}
	if err != nil {
		// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf(
			"engine: configure %s: %w", axis.Policy(), err,
		)
	}
	blocks, err := policyConfigurationBlockOutcomesFrom(
		outcomes.AccountBlocks, res, axis.Policy(),
	)
	if err != nil {
		return fwengine.PolicyConfigurationResult{}, err
	}
	return fwengine.PolicyConfigurationResult{AccountBlocks: blocks}, nil
}

// Current negative: internal/store/sqlite/balances.go:164.
func (r *realmStore) ListAccountsBlockingCurrencyChange(
	ctx context.Context, accounts []domain.AccountID,
) (value []fwstore.CurrencyChangeBlocker, resultErr error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	activeStatusIDs, err := r.activeOrderStatusIDs(ctx)
	if err != nil {
		return nil, err
	}
	activeStatusPlaceholders := strings.TrimRight(
		strings.Repeat("?,", len(activeStatusIDs)),
		",",
	)
	args := make([]any, 0, len(accounts)+len(activeStatusIDs)+3)
	accountWhere := ""
	if len(accounts) > 0 {
		placeholders := make([]string, 0, len(accounts))
		for _, account := range accounts {
			placeholders = append(placeholders, "?")
			args = append(args, account.String())
		}
		accountWhere = " WHERE code IN (" + strings.Join(placeholders, ", ") + ")"
	}
	args = append(args, activeStatusIDs...)
	args = append(args, domain.ScopeAccount, domain.ScopeAccountGroup, domain.ScopeGlobal)
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	query := `WITH candidates AS (
    SELECT * FROM account` + accountWhere + `
), balance_fields(kind) AS (
    VALUES ('available'), ('held'), ('incoming'), ('average_entry_price'),
           ('realized_pnl'), ('balance_pnl_halt')
), balance_values AS (
    SELECT a.code AS account, f.kind, ast.code AS asset,
           CASE f.kind
               WHEN 'available' THEN b.available
               WHEN 'held' THEN b.held
               WHEN 'incoming' THEN b.incoming
               WHEN 'average_entry_price' THEN b.average_entry_price
               WHEN 'realized_pnl' THEN b.realized_pnl
               WHEN 'balance_pnl_halt' THEN b.realized_pnl_halt_reason
           END AS value
    FROM candidates a
    JOIN balance b ON b.account_id = a.id
    JOIN asset ast ON ast.id = b.asset_id
    CROSS JOIN balance_fields f
), reasons AS (
    SELECT account, kind, asset, value, '' AS order_id, '' AS status,
           '' AS scope, '' AS group_code, '' AS axis, '' AS currency,
           '' AS lower_bound, '' AS upper_bound
    FROM balance_values
    WHERE value IS NOT NULL AND value <> '' AND (
        kind = 'balance_pnl_halt' OR value COLLATE DECIMAL <> '0'
    )
    UNION ALL
    SELECT code, 'account_pnl', '', pnl, '', '', '', '', '', '', '', ''
    FROM candidates
    WHERE pnl IS NOT NULL AND pnl <> '' AND pnl COLLATE DECIMAL <> '0'
    UNION ALL
    SELECT code, 'account_pnl_halt', '', pnl_halt_reason,
           '', '', '', '', '', '', '', ''
    FROM candidates WHERE pnl_halt_reason <> ''
    UNION ALL
    SELECT a.code, 'active_order', '', '', o.external_id, s.code,
           '', '', '', '', '', ''
    FROM candidates a
    JOIN order_record o ON o.account_id = a.id
    JOIN order_status s ON s.id = o.status_id
    WHERE o.status_id IN (` + activeStatusPlaceholders + `)
    UNION ALL
    SELECT a.code, 'pnl_limit', '', '', '', '', l.scope,
           COALESCE(g.code, ''), l.axis, c.code,
           COALESCE(l.lower_bound, ''), COALESCE(l.upper_bound, '')
    FROM candidates a
    JOIN limit_spot_funds_pnl_bound l ON (
        (l.scope = ? AND l.account_id = a.id) OR
        (l.scope = ? AND l.account_group_id = a.group_id) OR l.scope = ?
    )
    JOIN asset c ON c.id = l.currency_asset_id
    LEFT JOIN account_group g ON g.id = l.account_group_id
    WHERE l.lower_bound <> '' OR l.upper_bound <> ''
)
SELECT * FROM reasons
ORDER BY account, kind, asset, order_id, scope, group_code, axis`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list currency change blockers: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	result := make([]fwstore.CurrencyChangeBlocker, 0)
	for rows.Next() {
		var reason domain.CurrencyChangeReason
		if err := rows.Scan(
			&reason.Account, &reason.Kind, &reason.Asset, &reason.Value,
			&reason.Order, &reason.Status, &reason.Scope, &reason.Group,
			&reason.Axis, &reason.Currency, &reason.LowerBound, &reason.UpperBound,
		); err != nil {
			return nil, fmt.Errorf("store: scan currency change blocker: %w", err)
		}
		if len(result) == 0 || result[len(result)-1].Account != reason.Account {
			result = append(result, fwstore.CurrencyChangeBlocker{Account: reason.Account})
		}
		blocker := &result[len(result)-1]
		blocker.Reasons = append(blocker.Reasons, reason)
		if reason.Kind == "pnl_limit" {
			blocker.CurrencyValuedLimit = true
		} else {
			blocker.Other = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate currency change blockers: %w", err)
	}
	return result, nil
}

// Current negative: internal/store/sqlite/backup_prune.go:277.
func (rt *restoreTx) pruneLimits(
	ctx context.Context, selector backup.EntitySelector, accounts accountScope, data backup.Data,
) error {
	// Invalid archive keys add nothing here. restoreLimits validates the same
	// barriers later in this transaction and rejects them before commit.
	// Deferring their errors preserves the restore's validation order.
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	rateKeep := make(map[domain.LimitKey]bool, len(data.RateLimits))
	for _, l := range data.RateLimits {
		key, err := l.Key()
		if err != nil {
			continue
		}
		rateKeep[key] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_rate", domain.PolicyRateLimit, rateKeep); err != nil {
		return err
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	sizeKeep := make(map[domain.LimitKey]bool, len(data.OrderSizeLimits))
	for _, l := range data.OrderSizeLimits {
		key, err := l.Key()
		if err != nil {
			continue
		}
		sizeKeep[key] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_order_size", domain.PolicyOrderSizeLimit, sizeKeep); err != nil {
		return err
	}
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	positionKeep := make(map[domain.LimitKey]bool, len(data.PositionLimits))
	for _, l := range data.PositionLimits {
		key, err := l.Key()
		if err != nil {
			continue
		}
		positionKeep[key] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_position", domain.PolicyPositionLimit, positionKeep); err != nil {
		return err
	}
	spotFundsPnlBounds := data.SpotFundsPnlBounds()
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	spotFundsKeep := make(map[domain.LimitKey]bool, len(spotFundsPnlBounds))
	for _, l := range spotFundsPnlBounds {
		key, err := l.Key()
		if err != nil {
			continue
		}
		spotFundsKeep[key] = true
	}
	groupScope := make(map[string]bool, len(selector.Groups)+len(data.Accounts))
	for _, group := range selector.Groups {
		groupScope[group] = true
	}
	for _, account := range data.Accounts {
		if accounts.in(account.Code) && account.GroupCode != "" {
			groupScope[account.GroupCode] = true
		}
	}
	return rt.pruneSpotFundsPnlBoundsLimits(
		ctx,
		selector,
		accounts,
		groupScope,
		spotFundsKeep,
	)
}

// Current negative: framework/node/node.go:88.
func (t LimitTarget) Key() (domain.LimitKey, error) {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	return domain.NewLimitKey(
		t.Policy, t.Mode, t.Scope, t.Account, t.AccountGroup, t.Asset,
	)
}

func equalKeys(a, b domain.LimitKey) bool {
	// ok: limit-key-literal, limit-target-literal, limit-key-string-join, limit-key-axis-compare, limit-key-sql
	return a == b
}
