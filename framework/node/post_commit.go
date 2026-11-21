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

	"go.openpit.dev/officer/framework/domain"
)

type postCommitKey struct{}

type postCommitScope struct {
	callbacks []func() error
	finished  bool
}

// PostCommitNotifications defers synchronous notifications to the outer
// operation. Call finish after releasing every lock and completing the operation,
// including any venue send, and join its error with the operation's result.
// Nested node calls reuse the outer scope; direct node callers own their scope.
// Append callbacks only on the operation's goroutine and before finish.
// Work that outlives the operation uses detachPostCommit.
func PostCommitNotifications(ctx context.Context) (context.Context, func() error) {
	if _, ok := ctx.Value(postCommitKey{}).(*postCommitScope); ok {
		return ctx, func() error { return nil }
	}
	scope := &postCommitScope{}
	return context.WithValue(ctx, postCommitKey{}, scope), func() error {
		scope.finished = true
		var failures []error
		for _, callback := range scope.callbacks {
			failures = append(failures, callback())
		}
		scope.callbacks = nil
		return errors.Join(failures...)
	}
}

func requirePostCommitScope(ctx context.Context) (*postCommitScope, error) {
	scope, ok := ctx.Value(postCommitKey{}).(*postCommitScope)
	if !ok || scope == nil {
		return nil, fmt.Errorf("missing post-commit notification scope: %w", domain.ErrInvalid)
	}
	if scope.finished {
		return nil, fmt.Errorf("post-commit notification scope is finished: %w", domain.ErrInvalid)
	}
	return scope, nil
}

func deferPostCommit(ctx context.Context, callback func() error) error {
	scope, err := requirePostCommitScope(ctx)
	if err != nil {
		return err
	}
	scope.callbacks = append(scope.callbacks, callback)
	return nil
}

func detachPostCommit(ctx context.Context) context.Context {
	return context.WithValue(ctx, postCommitKey{}, nil)
}
