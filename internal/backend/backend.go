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

// Package backend implements the Pit Officer control-plane service. The service
// is the single entry point the MCP and HTTP surfaces call into. It depends
// only on the node.NodeRouter seam, never on a concrete engine or store, so the
// same control-plane logic runs over one in-process node or many remote shards.
package backend

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/node"
)

// Status is the aggregate health of the whole deployment, assembled for the
// operator dashboard from the health of every node behind the router.
type Status struct {
	// Nodes carries one health record per node, in router enumeration order.
	Nodes []node.Health
	// Healthy reports whether every node reported a live engine and a reachable
	// store.
	Healthy bool
}

// Service is the Pit Officer control plane. It is constructed once per process
// and is safe for concurrent use by the surface handlers.
type Service struct {
	router node.NodeRouter
}

// New constructs a Service over the given node router. The router is the only
// dependency: it is the seam through which the service reaches every execution
// target.
func New(router node.NodeRouter) *Service {
	return &Service{router: router}
}

// Status returns the aggregate health of every node behind the router, for the
// dashboard. It queries each node's health and reports the deployment as
// healthy only when all nodes are healthy.
func (s *Service) Status(ctx context.Context) (Status, error) {
	nodes := s.router.All()
	healths := make([]node.Health, 0, len(nodes))
	healthy := true
	for i, n := range nodes {
		health, err := n.Health(ctx)
		if err != nil {
			return Status{}, fmt.Errorf("backend: node %d health: %w", i, err)
		}
		if !health.Engine.Running || !health.Store.Reachable {
			healthy = false
		}
		healths = append(healths, health)
	}
	return Status{Nodes: healths, Healthy: healthy}, nil
}

// keyFor builds the routing key for an account in the default tenant. Tenant is
// an internal axis and is never exposed on a surface.
func keyFor(id domain.AccountID) node.Key {
	return node.Key{Tenant: domain.DefaultTenant, Account: id}
}

// ListAccounts returns every account aggregated across all nodes.
func (s *Service) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	accounts := make([]domain.Account, 0)
	for i, n := range s.router.All() {
		part, err := n.ListAccounts(ctx)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list accounts: %w", i, err)
		}
		accounts = append(accounts, part...)
	}
	return accounts, nil
}

// CreateAccount validates the id, routes to the owning node, and creates the
// account.
func (s *Service) CreateAccount(
	ctx context.Context, id domain.AccountID,
) (domain.Account, error) {
	if err := domain.ValidateAccountID(id); err != nil {
		return domain.Account{}, err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return domain.Account{}, fmt.Errorf("backend: route account: %w", err)
	}
	return n.CreateAccount(ctx, keyFor(id), auth.CallerFromContext(ctx))
}

// BlockAccount validates the id and blocks the account with reason.
func (s *Service) BlockAccount(
	ctx context.Context, id domain.AccountID, reason string,
) error {
	return s.setAccountBlocked(ctx, id, true, reason)
}

// UnblockAccount validates the id and unblocks the account.
func (s *Service) UnblockAccount(ctx context.Context, id domain.AccountID) error {
	return s.setAccountBlocked(ctx, id, false, "")
}

func (s *Service) setAccountBlocked(
	ctx context.Context, id domain.AccountID, blocked bool, reason string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return fmt.Errorf("backend: route account: %w", err)
	}
	return n.SetAccountBlocked(ctx, keyFor(id), blocked, reason, auth.CallerFromContext(ctx))
}

// GetAccountState validates the id and returns the account row and its
// account-scoped barriers.
func (s *Service) GetAccountState(
	ctx context.Context, id domain.AccountID,
) (domain.Account, []domain.Limit, error) {
	if err := domain.ValidateAccountID(id); err != nil {
		return domain.Account{}, nil, err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return domain.Account{}, nil, fmt.Errorf("backend: route account: %w", err)
	}
	return n.GetAccountState(ctx, keyFor(id))
}

// ListLimits returns the barriers that reference account, aggregated across all
// nodes. An empty account returns all barriers.
func (s *Service) ListLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.Limit, error) {
	limits := make([]domain.Limit, 0)
	for i, n := range s.router.All() {
		part, err := n.ListLimits(ctx, account)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list limits: %w", i, err)
		}
		limits = append(limits, part...)
	}
	return limits, nil
}

// PutLimit validates the barrier, checks the referenced account exists when the
// scope has an account axis, routes to the owning node, and upserts it.
func (s *Service) PutLimit(ctx context.Context, limit domain.Limit) error {
	limit.Target.Tenant = domain.DefaultTenant
	if err := domain.ValidateLimit(limit); err != nil {
		return err
	}

	n, err := s.router.Route(keyFor(limit.Target.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}

	if limit.Target.Account != "" {
		if _, _, err := n.GetAccountState(ctx, keyFor(limit.Target.Account)); err != nil {
			return err
		}
	}
	return n.PutLimit(ctx, limit, auth.CallerFromContext(ctx))
}

// DeleteLimit validates the target, routes to the owning node, and removes the
// barrier.
func (s *Service) DeleteLimit(ctx context.Context, target domain.LimitTarget) error {
	target.Tenant = domain.DefaultTenant
	if err := domain.ValidateLimit(domain.Limit{
		Target: target,
		Values: placeholderValues(target.Policy),
	}); err != nil {
		return err
	}

	n, err := s.router.Route(keyFor(target.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}
	return n.DeleteLimit(ctx, target, auth.CallerFromContext(ctx))
}

// placeholderValues returns a minimal valid value set for policy so the target
// half of a delete request can be validated by domain.ValidateLimit without the
// caller supplying values. Delete addresses a barrier by target only.
func placeholderValues(policy string) []domain.LimitValue {
	switch policy {
	case domain.PolicyRateLimit:
		return []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "1"},
			{Kind: domain.KindWindow, Value: "1s"},
		}
	case domain.PolicyOrderSizeLimit:
		return []domain.LimitValue{{Kind: domain.KindMaxQuantity, Value: "1"}}
	case domain.PolicyPnlBoundsKillSwitch:
		return []domain.LimitValue{{Kind: domain.KindLowerBound, Value: "0"}}
	default:
		return nil
	}
}

// ListAudit returns the most recent count audit rows aggregated across all
// nodes, newest first.
func (s *Service) ListAudit(
	ctx context.Context, count int,
) ([]domain.AuditRow, error) {
	rows := make([]domain.AuditRow, 0)
	for i, n := range s.router.All() {
		part, err := n.ListAudit(ctx, count)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list audit: %w", i, err)
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// ListAuditFiltered returns the most recent rows audit entries, newest first,
// optionally narrowed to an account and/or a source. The node surface exposes
// no audit filters, so the rows are aggregated and filtered here; count bounds
// the result after filtering. An empty account or source disables that filter.
func (s *Service) ListAuditFiltered(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.AuditRow, error) {
	rows, err := s.ListAudit(ctx, count)
	if err != nil {
		return nil, err
	}
	if account == "" && source == "" {
		return rows, nil
	}
	out := make([]domain.AuditRow, 0, len(rows))
	for _, row := range rows {
		if account != "" && row.Account != account {
			continue
		}
		if source != "" && row.Source != source {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// --- Account group and notes -----------------------------------------------

// SetAccountGroup validates the account id, routes to the owning node, and sets
// or clears (empty groupID) the account's group membership.
func (s *Service) SetAccountGroup(
	ctx context.Context, id domain.AccountID, groupID string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if groupID != "" {
		if err := domain.ValidateGroupID(groupID); err != nil {
			return err
		}
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return fmt.Errorf("backend: route account: %w", err)
	}
	return n.SetAccountGroup(ctx, keyFor(id), groupID, auth.CallerFromContext(ctx))
}

// SetAccountNotes validates the account id and notes, routes to the owning
// node, and replaces the account's free-form notes.
func (s *Service) SetAccountNotes(
	ctx context.Context, id domain.AccountID, notes string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if err := domain.ValidateNotes(notes); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return fmt.Errorf("backend: route account: %w", err)
	}
	return n.SetAccountNotes(ctx, keyFor(id), notes, auth.CallerFromContext(ctx))
}

// --- Groups ----------------------------------------------------------------

// groupNode resolves the node that owns the default tenant's groups. Groups are
// a tenant-level concern; in the single-node deployment one node owns them. It
// routes via an empty account key, which the local router always owns.
func (s *Service) groupNode() (node.Node, error) {
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return nil, fmt.Errorf("backend: route group: %w", err)
	}
	return n, nil
}

// CreateGroup validates the id and notes, defaults the tenant, and creates the
// group.
func (s *Service) CreateGroup(
	ctx context.Context, group domain.AccountGroup,
) error {
	group.Tenant = domain.DefaultTenant
	if err := domain.ValidateGroupID(group.ID); err != nil {
		return err
	}
	if err := domain.ValidateNotes(group.Notes); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.CreateGroup(ctx, group, auth.CallerFromContext(ctx))
}

// ListGroups returns every group in the default tenant.
func (s *Service) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	n, err := s.groupNode()
	if err != nil {
		return nil, err
	}
	groups, err := n.ListGroups(ctx, domain.DefaultTenant)
	if err != nil {
		return nil, fmt.Errorf("backend: list groups: %w", err)
	}
	return groups, nil
}

// GetGroup validates the id and returns the group and its member accounts. It
// maps a missing group onto domain.ErrNotFound.
func (s *Service) GetGroup(
	ctx context.Context, id string,
) (domain.AccountGroup, []domain.Account, error) {
	if err := domain.ValidateGroupID(id); err != nil {
		return domain.AccountGroup{}, nil, err
	}
	n, err := s.groupNode()
	if err != nil {
		return domain.AccountGroup{}, nil, err
	}
	group, accounts, ok, err := n.GetGroup(ctx, domain.DefaultTenant, id)
	if err != nil {
		return domain.AccountGroup{}, nil, fmt.Errorf("backend: get group: %w", err)
	}
	if !ok {
		return domain.AccountGroup{}, nil, fmt.Errorf("group %q: %w", id, domain.ErrNotFound)
	}
	return group, accounts, nil
}

// SetGroupNotes validates the id and notes and replaces the group's notes.
func (s *Service) SetGroupNotes(ctx context.Context, id, notes string) error {
	if err := domain.ValidateGroupID(id); err != nil {
		return err
	}
	if err := domain.ValidateNotes(notes); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetGroupNotes(ctx, domain.DefaultTenant, id, notes, auth.CallerFromContext(ctx))
}

// SetGroupBlocked validates the id and blocks or unblocks the group with reason.
func (s *Service) SetGroupBlocked(
	ctx context.Context, id string, blocked bool, reason string,
) error {
	if err := domain.ValidateGroupID(id); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetGroupBlocked(ctx, domain.DefaultTenant, id, blocked, reason, auth.CallerFromContext(ctx))
}

// DeleteGroup validates the id and removes the group.
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	if err := domain.ValidateGroupID(id); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.DeleteGroup(ctx, domain.DefaultTenant, id, auth.CallerFromContext(ctx))
}

// --- Spot funds ------------------------------------------------------------

// ApplyAdjustment validates the account id and asset, routes to the owning
// node, and applies one spot-funds adjustment. The returned record carries the
// accepted-or-rejected outcome; a policy reject is a successful call, not an
// error. No existence check is performed on the account: the engine creates the
// balance on first adjustment.
func (s *Service) ApplyAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := domain.ValidateAdjustmentRequest(req); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	n, err := s.router.Route(keyFor(account))
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("backend: route account: %w", err)
	}
	return n.ApplyAdjustment(ctx, keyFor(account), req, auth.CallerFromContext(ctx))
}

// ListBalances returns the balance rows for the default tenant, optionally
// narrowed to a non-empty account and/or asset. It aggregates across nodes.
func (s *Service) ListBalances(
	ctx context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	balances := make([]domain.Balance, 0)
	for i, n := range s.router.All() {
		part, err := n.ListBalances(ctx, domain.DefaultTenant, account, asset)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list balances: %w", i, err)
		}
		balances = append(balances, part...)
	}
	return balances, nil
}

// ListAdjustments validates the account id and returns the most recent n
// adjustments for the account, newest first; an empty source returns all.
func (s *Service) ListAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return nil, err
	}
	target, err := s.router.Route(keyFor(account))
	if err != nil {
		return nil, fmt.Errorf("backend: route account: %w", err)
	}
	return target.ListAdjustments(ctx, domain.DefaultTenant, account, source, n)
}

// ListAllAdjustments returns the most recent n adjustments aggregated across all
// nodes and accounts, optionally narrowed to a non-empty account and/or source.
// It backs GET /adjustments. Per-node results are already newest-first; the
// merged slice is sorted newest-first and bounded to n.
func (s *Service) ListAllAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
		return s.ListAdjustments(ctx, account, source, n)
	}
	recs := make([]domain.AccountAdjustmentRecord, 0)
	for i, target := range s.router.All() {
		part, err := target.ListAdjustments(ctx, domain.DefaultTenant, "", source, n)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list adjustments: %w", i, err)
		}
		recs = append(recs, part...)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID > recs[j].ID })
	if n > 0 && len(recs) > n {
		recs = recs[:n]
	}
	return recs, nil
}

// --- Trading ---------------------------------------------------------------

// SubmitOrder validates the order's account and assets, routes to the owning
// node, and runs the engine pre-trade. The returned order carries the recorded
// lifecycle status, including a rejected status; an engine reject is a
// successful call, not an error.
func (s *Service) SubmitOrder(
	ctx context.Context, o domain.Order,
) (domain.Order, error) {
	o.Tenant = domain.DefaultTenant
	// Officer validates only the boundary id/asset formats; the engine enforces
	// the real trading rules. Existence is never checked: any well-formed account
	// or asset is accepted (existing or not), per the surface contract.
	if err := domain.ValidateAccountID(o.Account); err != nil {
		return domain.Order{}, err
	}
	if err := domain.ValidateAsset(o.BaseAsset); err != nil {
		return domain.Order{}, err
	}
	if err := domain.ValidateAsset(o.QuoteAsset); err != nil {
		return domain.Order{}, err
	}
	n, err := s.router.Route(keyFor(o.Account))
	if err != nil {
		return domain.Order{}, fmt.Errorf("backend: route order: %w", err)
	}
	return n.SubmitOrder(ctx, keyFor(o.Account), o, auth.CallerFromContext(ctx))
}

// ApplyExecutionReport validates the fill's account and assets, routes to the
// owning node, and settles the fill through the engine.
func (s *Service) ApplyExecutionReport(
	ctx context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	if err := domain.ValidateAccountID(in.Account); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	if err := domain.ValidateAsset(in.BaseAsset); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	if err := domain.ValidateAsset(in.QuoteAsset); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	n, err := s.router.Route(keyFor(in.Account))
	if err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("backend: route report: %w", err)
	}
	return n.ApplyExecutionReport(ctx, keyFor(in.Account), in, auth.CallerFromContext(ctx))
}

// GetOrder returns the order with its events and trades. It maps a missing
// order onto the node's domain.ErrNotFound.
func (s *Service) GetOrder(
	ctx context.Context, id int64,
) (domain.OrderDetail, error) {
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return domain.OrderDetail{}, fmt.Errorf("backend: route order: %w", err)
	}
	return n.GetOrder(ctx, domain.DefaultTenant, id)
}

// ListOrders returns the most recent n orders, optionally narrowed to a
// non-empty account and/or source, newest first, aggregated across nodes.
func (s *Service) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Order, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	orders := make([]domain.Order, 0)
	for i, target := range s.router.All() {
		part, err := target.ListOrders(ctx, domain.DefaultTenant, account, source, n)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list orders: %w", i, err)
		}
		orders = append(orders, part...)
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].ID > orders[j].ID })
	if n > 0 && len(orders) > n {
		orders = orders[:n]
	}
	return orders, nil
}

// ListTrades returns the most recent n trades, optionally narrowed to a
// non-empty account and/or source, newest first, aggregated across nodes.
func (s *Service) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Trade, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	trades := make([]domain.Trade, 0)
	for i, target := range s.router.All() {
		part, err := target.ListTrades(ctx, domain.DefaultTenant, account, source, n)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list trades: %w", i, err)
		}
		trades = append(trades, part...)
	}
	sort.Slice(trades, func(i, j int) bool { return trades[i].ID > trades[j].ID })
	if n > 0 && len(trades) > n {
		trades = trades[:n]
	}
	return trades, nil
}

// --- Dashboard / service ---------------------------------------------------

// Counts is the headline tally on the operator overview: how many accounts,
// groups, and risk barriers the deployment holds.
type Counts struct {
	// Accounts is the number of accounts across all nodes.
	Accounts int
	// Groups is the number of account groups.
	Groups int
	// Limits is the number of risk barriers.
	Limits int
}

// ActivityKind classifies one recent-activity entry on the overview feed.
type ActivityKind string

const (
	// ActivityKindAudit is a control-plane audit action.
	ActivityKindAudit ActivityKind = "audit"
	// ActivityKindOrder is an order submission.
	ActivityKindOrder ActivityKind = "order"
	// ActivityKindAdjustment is a spot-funds adjustment.
	ActivityKindAdjustment ActivityKind = "adjustment"
)

// Activity is one recent-activity entry, derived from the persisted log (audit
// rows, orders, adjustments) rather than any in-memory session registry. Newest
// entries come first.
type Activity struct {
	// At is when the underlying action was recorded.
	At time.Time
	// Source is the channel that originated the action.
	Source domain.Source
	// Kind classifies the entry (audit, order, adjustment).
	Kind ActivityKind
	// Ref is the human-readable reference (account id, order id).
	Ref string
	// Summary is a short description of the action.
	Summary string
}

// Overview is the operator dashboard summary: headline counts plus a recent
// activity feed attributed by source.
type Overview struct {
	// Counts are the headline tallies.
	Counts Counts
	// Activity is the recent-activity feed, newest first.
	Activity []Activity
}

// overviewActivityCap bounds the recent-activity feed assembled by Overview.
const overviewActivityCap = 20

// Overview assembles the operator dashboard summary: the counts of accounts,
// groups, and barriers, and a source-attributed recent-activity feed merged
// from the most recent audit rows, orders, and adjustments. The feed is derived
// from the persisted log (newest first, capped), not an in-memory registry.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return Overview{}, err
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return Overview{}, err
	}
	limits, err := s.ListLimits(ctx, "")
	if err != nil {
		return Overview{}, err
	}

	audit, err := s.ListAudit(ctx, overviewActivityCap)
	if err != nil {
		return Overview{}, err
	}
	orders, err := s.ListOrders(ctx, "", "", overviewActivityCap)
	if err != nil {
		return Overview{}, err
	}
	adjustments, err := s.ListAllAdjustments(ctx, "", "", overviewActivityCap)
	if err != nil {
		return Overview{}, err
	}

	activity := mergeActivity(audit, orders, adjustments)
	return Overview{
		Counts: Counts{
			Accounts: len(accounts),
			Groups:   len(groups),
			Limits:   len(limits),
		},
		Activity: activity,
	}, nil
}

// mergeActivity folds recent audit rows, orders, and adjustments into one feed
// sorted newest-first by timestamp and bounded to overviewActivityCap.
func mergeActivity(
	audit []domain.AuditRow, orders []domain.Order, adjustments []domain.AccountAdjustmentRecord,
) []Activity {
	out := make([]Activity, 0, len(audit)+len(orders)+len(adjustments))
	for _, row := range audit {
		out = append(out, Activity{
			At:      row.At,
			Source:  row.Source,
			Kind:    ActivityKindAudit,
			Ref:     string(row.Account),
			Summary: row.Detail,
		})
	}
	for _, o := range orders {
		out = append(out, Activity{
			At:      o.At,
			Source:  o.Source,
			Kind:    ActivityKindOrder,
			Ref:     strconv.FormatInt(o.ID, 10),
			Summary: fmt.Sprintf("%s %s %s/%s %s", o.Side, o.AmountValue, o.BaseAsset, o.QuoteAsset, o.Status),
		})
	}
	for _, a := range adjustments {
		status := domain.AdjustmentStatusAccepted
		if a.Rejected != nil {
			status = domain.AdjustmentStatusRejected
		}
		out = append(out, Activity{
			At:      a.At,
			Source:  a.Source,
			Kind:    ActivityKindAdjustment,
			Ref:     string(a.Account),
			Summary: fmt.Sprintf("adjustment %s %s", a.Request.Asset, status),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > overviewActivityCap {
		out = out[:overviewActivityCap]
	}
	return out
}

// ServiceDatabase is the database facet of ServiceInfo: where the store lives
// and whether it answered its last probe.
type ServiceDatabase struct {
	// Path is the on-disk database location.
	Path string
	// Reachable reports whether the store responded to its last health probe.
	Reachable bool
}

// ServiceInfo is the static identity and build posture of the running service.
// Pit Officer is monolithic, so it reports one engine build profile and one
// database rather than per-node detail.
type ServiceInfo struct {
	// Name is the service name.
	Name string
	// EngineVersion is the engine SDK/runtime version.
	EngineVersion string
	// EngineBuildProfile is the engine build profile (for example "release").
	EngineBuildProfile string
	// Database is where the store lives and whether it is reachable.
	Database ServiceDatabase
	// Release reports whether the engine build profile is "release".
	Release bool
}

// ServiceInfo reports the service identity and build posture. The engine
// version, build profile, and the database path/reachability are sourced from
// the node health already gathered by Status; in the single-node deployment the
// first node supplies them.
func (s *Service) ServiceInfo(ctx context.Context) (ServiceInfo, error) {
	status, err := s.Status(ctx)
	if err != nil {
		return ServiceInfo{}, err
	}
	info := ServiceInfo{Name: "Pit Officer"}
	if len(status.Nodes) > 0 {
		h := status.Nodes[0]
		info.EngineVersion = h.Engine.Version
		info.EngineBuildProfile = h.Engine.BuildProfile
		info.Release = h.Engine.BuildProfile == "release"
		info.Database = ServiceDatabase{
			Path:      h.Store.Path,
			Reachable: h.Store.Reachable,
		}
	}
	return info, nil
}
