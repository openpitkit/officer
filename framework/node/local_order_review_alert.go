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

// OrderReviewObserver receives committed review transitions outside node locks.
// Callback contexts are detached from cancellation and the completed scope.
type OrderReviewObserver interface {
	OrderAwaitingReview(ctx context.Context, order domain.Order, mark domain.ReviewMark, caller domain.Caller) error
	OrderReviewDecided(ctx context.Context, order domain.Order, caller domain.Caller) error
}

// OrderReviewObserverHost optionally installs the node's review observer.
// InstallOrderReviewObserver must be called before the node serves orders.
// Installing later races with order paths, which read the observer without
// locking.
type OrderReviewObserverHost interface {
	InstallOrderReviewObserver(OrderReviewObserver) error
}

func (n *localNode) InstallOrderReviewObserver(observer OrderReviewObserver) error {
	if observer == nil {
		return fmt.Errorf("missing order review observer: %w", domain.ErrInvalid)
	}
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()
	if n.orderReviewObserver != nil {
		return fmt.Errorf("order review observer already installed: %w", domain.ErrInvalid)
	}
	n.orderReviewObserver = observer
	return nil
}

func (n *localNode) publishOrderAwaitingReview(
	ctx context.Context, order domain.Order, mark domain.ReviewMark, caller domain.Caller,
) error {
	observer := n.orderReviewObserver
	if observer == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	return deferPostCommit(ctx, func() error {
		if err := observer.OrderAwaitingReview(detachPostCommit(ctx), order, mark, caller); err != nil {
			return &domain.OrderReviewAlertError{
				Step: domain.OrderReviewAlertStepPublish, Cause: err,
			}
		}
		return nil
	})
}

func (n *localNode) publishOrderReviewDecided(
	ctx context.Context, order domain.Order, caller domain.Caller,
) error {
	observer := n.orderReviewObserver
	if observer == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	return deferPostCommit(ctx, func() error {
		if err := observer.OrderReviewDecided(detachPostCommit(ctx), order, caller); err != nil {
			return &domain.OrderReviewAlertError{
				Step: domain.OrderReviewAlertStepClose, Cause: err,
			}
		}
		return nil
	})
}

func (n *localNode) finishSubmitReviewNotifications(
	ctx context.Context, finish func() error, order *domain.Order,
	reviews *[]domain.ReviewMark, caller domain.Caller, resultErr *error,
) {
	if *resultErr == nil && order.Status == domain.OrderStatusAwaitingReview {
		if len(*reviews) == 0 {
			*resultErr = fmt.Errorf("order awaiting review has no review mark: %w", domain.ErrInvalid)
		} else {
			*resultErr = n.publishOrderAwaitingReview(ctx, *order, (*reviews)[0], caller)
		}
	}
	finishReviewNotificationScope(finish, resultErr)
}

func (n *localNode) finishDecisionReviewNotifications(
	ctx context.Context, finish func() error, order *domain.Order,
	caller domain.Caller, resultErr *error,
) {
	if *resultErr == nil && order.Status != domain.OrderStatusAwaitingReview {
		*resultErr = n.publishOrderReviewDecided(ctx, *order, caller)
	}
	finishReviewNotificationScope(finish, resultErr)
}

func finishReviewNotificationScope(finish func() error, resultErr *error) {
	if err := finish(); err != nil {
		*resultErr = errors.Join(*resultErr, err)
	}
}
