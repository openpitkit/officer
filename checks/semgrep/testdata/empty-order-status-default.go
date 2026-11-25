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

package fixtures

import (
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

func cancelStatus(event domain.OrderEvent) domain.OrderStatus {
	status := domain.OrderStatus(event.Payload.OrderStatus)
	// ruleid: empty-order-status-default
	if status == "" {
		status = domain.OrderStatusCancelled
	}
	return status
}

func immediateStatus(settled domain.Order) domain.Order {
	// ruleid: empty-order-status-default
	if settled.Status == "" {
		settled.Status = domain.OrderStatusFilled
	}
	return settled
}

func reportStatus(persistence engine.ExecutionReportPersistence, targetStatus domain.OrderStatus) {
	// ruleid: empty-order-status-default
	if persistence.OrderStatus == "" {
		persistence.OrderStatus = targetStatus
	}
}

func reversedStatus(event domain.OrderEvent, settled domain.Order, persistence engine.ExecutionReportPersistence, replacement domain.OrderStatus) {
	var status = domain.OrderStatus(event.Payload.OrderStatus)
	// ruleid: empty-order-status-default
	if "" == status {
		status = replacement
	}
	// ruleid: empty-order-status-default
	if "" == settled.Status {
		settled.Status = replacement
	}
	// ruleid: empty-order-status-default
	if "" == persistence.OrderStatus {
		persistence.OrderStatus = domain.OrderStatusFilled
	}
}

func requireStatus(event domain.OrderEvent, settled domain.Order, persistence engine.ExecutionReportPersistence) error {
	status := domain.OrderStatus(event.Payload.OrderStatus)
	// ok: empty-order-status-default
	if status == "" {
		return fmt.Errorf("cancel event missing order status")
	}
	// ok: empty-order-status-default
	if settled.Status == "" {
		return fmt.Errorf("immediate persistence missing order status")
	}
	// ok: empty-order-status-default
	if "" == persistence.OrderStatus {
		return fmt.Errorf("execution report event missing order status")
	}
	return nil
}

func unrelatedEmptyValue(value string, order domain.Order) {
	// ok: empty-order-status-default
	if value == "" {
		value = "label"
	}
	// ok: empty-order-status-default
	if order.Status == "" {
		order.ExternalID = "different-field"
	}
}
