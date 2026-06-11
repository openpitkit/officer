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

// Package engine defines the adapter seam between the Pit Officer control plane
// and the OpenPit binding. The concrete adapter that satisfies Engine wraps a
// *openpit.Engine (go.openpit.dev/openpit); it owns the engine's lifecycle and
// translates between Pit Officer's domain types and the binding's param/model
// types.
//
// Pit Officer adds no risk logic in front of the engine: the adapter builds the
// one engine at process start, configures policies, blocks accounts, observes,
// and stops the engine. All policy evaluation stays inside the engine.
//
// One engine handle is constructed by BuildOpenPitEngine and lives until Stop.
// Later limit changes are applied dynamically through the binding's runtime
// Configure surface; the adapter never rebuilds its own handle. Where the SDK
// cannot yet express a change (for example adding the first barrier of an
// unregistered policy, or adding/removing a rate-limit barrier, which is
// patch-only), ConfigurePolicy returns an error wrapping domain.ErrNotImplemented.
// The control-plane node treats that as a signal to rebuild the engine from the
// current store snapshot and swap the handle (see node.localNode). A rebuild is
// safe here because the control plane holds no externally-observable in-flight
// reservations: pre-trade reservations never escape SubmitOrder, so a fresh
// handle reconstructed from the persisted snapshot is equivalent to the old one.
package engine

import (
	"context"
	"fmt"

	"go.openpit.dev/officer/internal/domain"
)

// ErrCheckUnsupported is returned by Engine.CheckOrder. The OpenPit SDK has no
// non-mutating dry-run yet, so the check seam is a stub on every adapter. It
// wraps domain.ErrNotImplemented so the surface layers map it uniformly.
var ErrCheckUnsupported = fmt.Errorf(
	"engine: non-mutating order check unsupported: %w", domain.ErrNotImplemented)

// Snapshot is the engine state the control plane builds the engine from at
// process start: accounts' blocked state and the complete set of risk barriers.
// BuildOpenPitEngine consumes it once. A Snapshot is plain data with no native
// handles, so the store layer can assemble it without cgo.
type Snapshot struct {
	// Accounts are the accounts to apply, including their blocked state and
	// block reason. Group membership is read from each account's GroupID.
	Accounts []domain.Account
	// Limits are the complete set of risk barriers across all policies.
	Limits []domain.Limit
	// Groups are the persisted account groups, carrying their blocked state and
	// reason. Membership lives on Accounts; this seeds group blocks only.
	Groups []domain.AccountGroup
	// Balances are the persisted per-(account, asset) holdings to seed into the
	// spot-funds policy as absolute adjustments.
	Balances []domain.Balance
}

// AdjustmentResult is the outcome of one ApplyAccountAdjustment call. Exactly
// one of Accepted/Rejected is non-nil, mirroring the binding's accept/reject
// split for a single-asset adjustment.
type AdjustmentResult struct {
	// Accepted carries the resulting per-field delta and absolute on accept.
	Accepted *domain.AdjustmentOutcomeAccepted
	// Rejected carries the first reject on reject.
	Rejected *domain.AdjustmentOutcomeRejected
}

// OrderResult is the outcome of one SubmitOrder pre-trade call. On accept the
// reservation is committed inside the adapter and LockPrices holds the lock
// prices captured before commit; on reject Rejects holds the engine rejects.
type OrderResult struct {
	// LockPrices are the reservation lock prices captured as decimal strings
	// before the reservation was committed. Empty when the order locked nothing.
	LockPrices []string
	// Rejects are the engine pre-trade rejects; non-empty only when not accepted.
	Rejects []domain.OrderReject
	// Accepted reports whether the pre-trade passed and was committed.
	Accepted bool
}

// ExecutionReportResult is the outcome of one ApplyExecutionReport call: the
// account blocks the engine recorded and any per-asset adjustment outcomes
// policies produced.
type ExecutionReportResult struct {
	// Blocks are the account blocks the engine recorded for this report.
	Blocks []domain.ExecutionAccountBlock
	// Outcomes are the per-asset adjustment outcomes policies produced.
	Outcomes []domain.AdjustmentOutcomeAccepted
}

// BuildFunc builds the one engine from the seed snapshot. The local node calls
// it once during construction; production wires it to BuildOpenPitEngine bound
// to a runtime-library path. Tests substitute a fake.
type BuildFunc func(snap Snapshot) (Engine, error)

// Health reports the observable condition of an engine adapter for the
// dashboard and health checks.
type Health struct {
	// Version is the engine SDK/runtime version, as reported by Engine.Version.
	Version string
	// BuildProfile is the engine build profile (for example "release"), as
	// reported by Engine.BuildProfile.
	BuildProfile string
	// Running reports whether the underlying engine handle is live (built and
	// not yet stopped).
	Running bool
}

// Engine is the control plane's view of one OpenPit engine instance. The
// concrete implementation wraps *openpit.Engine.
//
// Implementations are not required to be safe for concurrent use unless the
// underlying engine was built with a concurrent sync policy; callers must honor
// the binding's threading contract.
type Engine interface {
	// Version returns the engine SDK/runtime version string.
	Version() string

	// BuildProfile returns the engine build profile string.
	BuildProfile() string

	// Running reports whether the engine handle is live (built and not
	// stopped).
	Running() bool

	// ConfigurePolicy reconfigures one policy from its complete barrier set on
	// the live engine handle via the binding's Configure surface. limits is the
	// full set of barriers for policy (every target whose policy equals it);
	// policy is one of the domain policy ids.
	//
	// The change is applied dynamically: the engine is never rebuilt. For
	// order_size_limit and pnl_bounds_kill_switch the supplied axes are replaced
	// wholesale; order_size_limit additionally returns an error wrapping
	// domain.ErrNotImplemented when it would drop a broker barrier, which the
	// Configure surface cannot clear in isolation. For rate_limit, whose
	// Configure is patch-only, an unchanged barrier-key set is retuned; a key set
	// that gained or lost a barrier returns an error wrapping
	// domain.ErrNotImplemented. Configuring a policy that was not registered at
	// build time, or removing the last barrier of a registered policy, likewise
	// returns domain.ErrNotImplemented. It returns an error if the engine is not
	// running or if the configuration cannot be applied.
	ConfigurePolicy(ctx context.Context, policy string, limits []domain.Limit) error

	// BlockAccount kill-switches the account in the engine, gating its pre-trade
	// orders until it is unblocked. reason is recorded with the block.
	BlockAccount(ctx context.Context, id domain.AccountID, reason string) error

	// UnblockAccount lifts the engine block on the account. Unblocking an
	// account that is not blocked is a no-op.
	UnblockAccount(ctx context.Context, id domain.AccountID) error

	// ApplyAccountAdjustment applies one spot-funds adjustment for account on the
	// live engine and returns its accept/reject outcome. req carries one balance
	// operation on req.Asset with optional average-entry-price and per-field
	// absolute|delta amounts and optional bounds.
	ApplyAccountAdjustment(
		ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
	) (AdjustmentResult, error)

	// SubmitOrder runs the pre-trade pipeline for o on the live engine. On accept
	// it captures the reservation lock prices, commits the reservation, and
	// returns Accepted with those prices; on reject it returns the engine rejects.
	// The native reservation never escapes the adapter.
	SubmitOrder(ctx context.Context, o domain.Order) (OrderResult, error)

	// ApplyExecutionReport settles a fill on the live engine and returns the
	// account blocks the engine recorded plus any adjustment outcomes.
	ApplyExecutionReport(
		ctx context.Context, in domain.ExecutionReportInput,
	) (ExecutionReportResult, error)

	// RegisterGroup atomically registers accounts into the group identified by
	// groupID on the live engine. An account belongs to exactly one non-default
	// group.
	RegisterGroup(ctx context.Context, accounts []domain.AccountID, groupID string) error

	// UnregisterGroup atomically removes accounts from the group identified by
	// groupID on the live engine.
	UnregisterGroup(ctx context.Context, accounts []domain.AccountID, groupID string) error

	// BlockGroup kill-switches every account in groupID on the live engine,
	// recording reason. Re-blocking refreshes the reason.
	BlockGroup(ctx context.Context, groupID, reason string) error

	// UnblockGroup lifts the engine block on groupID. Unblocking an unblocked
	// group is a no-op.
	UnblockGroup(ctx context.Context, groupID string) error

	// CheckOrder runs a non-mutating order check. It is a stub: it always
	// returns ErrCheckUnsupported because the SDK has no non-mutating dry-run
	// yet. It is not wired to any surface.
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)

	// Stop halts the engine and releases the underlying native resources. After
	// Stop the engine is no longer usable. Stop is idempotent.
	Stop()
}
