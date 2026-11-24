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

func requirePostCommitScope(ctx context.Context) (*postCommitScope, error) {
	scope, ok := ctx.Value(postCommitKey{}).(*postCommitScope)
	if !ok || scope == nil {
		// ruleid: node-internal-invalid-input
		return nil, fmt.Errorf("missing post-commit scope: %w", domain.ErrInvalid)
	}
	return scope, nil
}

func deferPostCommit(ctx context.Context, callback func() error) error {
	// ruleid: node-internal-invalid-input
	return domain.ErrInvalid
}

func (n *localNode) reconcile() error {
	return n.fatalReconciliation("reconcile",
		// ruleid: node-internal-invalid-input
		fmt.Errorf("inconsistent state: %w", domain.ErrInvalid))
}

func (n *localNode) reconcileLocal() error {
	// ruleid: node-internal-invalid-input
	err := fmt.Errorf("inconsistent state: %w", domain.ErrInvalid)
	return n.fatalReconciliation("reconcile", err)
}

func (n *localNode) validate(req Request) error {
	if req.Account == "" {
		// ok: node-internal-invalid-input
		return fmt.Errorf("account required: %w", domain.ErrInvalid)
	}
	return nil
}

func (n *localNode) reconcileInternal(err error) error {
	// ok: node-internal-invalid-input
	return n.fatalReconciliation("reconcile", domain.Internal(err))
}

func detachPostCommit(ctx context.Context) (func() error, error) {
	// ok: node-internal-invalid-input
	return nil, domain.Internal(fmt.Errorf("finished scope: %w", domain.ErrInvalid))
}

func PostCommitNotifications(ctx context.Context) error {
	// ruleid: node-internal-invalid-input
	return errors.Join(errors.New("scope missing"), domain.ErrInvalid)
}

func (n *localNode) reconcileJoined() error {
	// ruleid: node-internal-invalid-input
	err := errors.Join(errors.New("inconsistent state"), domain.ErrInvalid)
	return n.fatalReconciliation("reconcile", err)
}
