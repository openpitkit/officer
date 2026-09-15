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

package backend

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// Counts is the headline tally on the operator overview: how many accounts,
// groups, and risk barriers the deployment holds.
type Counts struct {
	// Accounts is the number of accounts.
	Accounts int
	// AccountsActive is the number of accounts that are not blocked.
	AccountsActive int
	// Groups is the number of account groups.
	Groups int
	// GroupsActive is the number of account groups that are not blocked.
	GroupsActive int
	// Limits is the number of risk barriers.
	Limits int
	// OrdersActive is the number of non-terminal orders.
	OrdersActive int
	// OrdersToday is the number of orders recorded since the caller-supplied
	// today boundary.
	OrdersToday int
	// OrdersTotal is the total number of orders recorded.
	OrdersTotal int
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
// groups, barriers, and orders (today / total), and a source-attributed
// recent-activity feed merged from the most recent audit rows, orders, and
// adjustments. The feed is derived from the persisted log (newest first,
// capped), not an in-memory registry. The since boundary delimits "today" for
// the OrdersToday tally; the caller supplies it (request-local or server-local
// start-of-day).
func (s *Service) Overview(ctx context.Context, since time.Time) (Overview, error) {
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
	limitCount := len(limits.RateLimits) + len(limits.OrderSizeLimits) +
		len(limits.SpotFundsPnlBoundsLimits)

	ordersTotal, err := s.node.CountOrders(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("backend: count orders: %w", err)
	}
	ordersActive, err := s.node.CountActiveOrders(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("backend: count active orders: %w", err)
	}
	ordersToday, err := s.node.CountOrdersSince(ctx, since)
	if err != nil {
		return Overview{}, fmt.Errorf("backend: count orders since: %w", err)
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

	// An account counts as active only when it can actually trade: the engine
	// rejects every order from a member of a blocked group, so the group tier
	// is joined in here as it is on the account read paths.
	blocks := domain.NewGroupBlockIndex(groups)
	accountsActive := 0
	for _, account := range accounts {
		if !blocks.Resolve(account).Blocked {
			accountsActive++
		}
	}
	groupsActive := 0
	for _, group := range groups {
		if !group.Blocked {
			groupsActive++
		}
	}

	activity := mergeActivity(audit, orders, adjustments)
	return Overview{
		Counts: Counts{
			Accounts:       len(accounts),
			AccountsActive: accountsActive,
			Groups:         len(groups),
			GroupsActive:   groupsActive,
			Limits:         limitCount,
			OrdersActive:   ordersActive,
			OrdersToday:    ordersToday,
			OrdersTotal:    ordersTotal,
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
			Ref:     o.ExternalID.String(),
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
// the node health reported by Status.
func (s *Service) ServiceInfo(ctx context.Context) (ServiceInfo, error) {
	status, err := s.Status(ctx)
	if err != nil {
		return ServiceInfo{}, err
	}
	h := status.Node
	return ServiceInfo{
		Name:               "Pit Officer",
		EngineVersion:      h.Engine.Version,
		EngineBuildProfile: h.Engine.BuildProfile,
		Release:            h.Engine.BuildProfile == "release",
		Database: ServiceDatabase{
			Path:      h.Store.Path,
			Reachable: h.Store.Reachable,
		},
	}, nil
}
