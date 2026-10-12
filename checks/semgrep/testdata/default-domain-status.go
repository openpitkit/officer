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

// Package fixture holds the regression cases of the default-domain-status
// rule. The violations are deliberate; the file is never compiled.
package fixture

func switchDefault(s string) VenueStatus {
	switch s {
	case "filled":
		return VenueStatusFilled
	case "canceled", "expired":
		return VenueStatusCancelled
	// ruleid: default-domain-status
	default:
		return VenueStatusOpen
	}
}

func switchDefaultWithError(s string) (fwtrading.VenueStatus, error) {
	switch s {
	case "filled":
		return fwtrading.VenueStatusFilled, nil
	// ruleid: default-domain-status
	default:
		note(s)

		return fwtrading.VenueStatusOpen, nil
	}
}

func taglessDefault(s string) domain.AuditAction {
	switch {
	case s == "restart":
		return domain.AuditActionRestartService
	// ruleid: default-domain-status
	default:
		return domain.AuditActionStopService
	}
}

func assignedDefault(s string) (status VenueStatus) {
	switch s {
	case "filled":
		status = VenueStatusFilled
	// ruleid: default-domain-status
	default:
		status = VenueStatusOpen
	}
	return status
}

func mapMiss(s string) VenueStatus {
	status, ok := statuses[s]
	// ruleid: default-domain-status
	if !ok {
		return VenueStatusOpen
	}
	return status
}

func mapMissInline(s string) domain.OrderStatus {
	// ruleid: default-domain-status
	if status, found := orderStatuses[s]; !found {
		return domain.OrderStatusAccepted
	}
	return domain.OrderStatusFilled
}

func mapMissElse(s string, closed bool) (status VenueStatus) {
	status, exists := statuses[s]
	if closed {
		status = VenueStatusCancelled
		// ruleid: default-domain-status
	} else if !exists {
		status = VenueStatusOpen
	}
	return status
}

func mapHitFallthrough(s string) VenueStatus {
	// ruleid: default-domain-status
	if status, ok := statuses[s]; ok {
		return status
	}
	return VenueStatusOpen
}

func mapHitFallthroughAfterBlankLine(s string) ConnectionState {
	// ruleid: default-domain-status
	if state, known := connectionStates[s]; known {
		return state
	}

	return ConnectionRunning
}

func trailingCommentDefault(s string) VenueStatus {
	switch s {
	case "filled":
		return VenueStatusFilled
	// ruleid: default-domain-status
	default: // unmapped
		return VenueStatusOpen
	}
}

func trailingCommentMiss(s string) VenueStatus {
	status, ok := statuses[s]
	// ruleid: default-domain-status
	if !ok { // unmapped
		return VenueStatusOpen
	}
	return status
}

func mapHitFallthroughAfterComment(s string) VenueStatus {
	// ruleid: default-domain-status
	if status, ok := statuses[s]; ok { // mapped
		return status
	}
	// The venue sent a status the map does not hold.

	return VenueStatusOpen
}

func mapHitElse(s string) VenueStatus {
	// ruleid: default-domain-status
	if status, ok := statuses[s]; ok {
		return status
	} else {
		return VenueStatusOpen
	}
}

func mapHitElseAssigned(s string) (status VenueStatus) {
	// ruleid: default-domain-status
	if mapped, found := statuses[s]; found {
		status = mapped
	} else { // unmapped
		status = VenueStatusOpen
	}
	return status
}

func statusNotFirst(s string) (int, VenueStatus) {
	switch s {
	case "filled":
		return 1, VenueStatusFilled
	// ruleid: default-domain-status
	default:
		return 0, VenueStatusOpen
	}
}

func indexTarget(byID map[string]fwtrading.VenueStatus, id, s string) {
	switch s {
	case "filled":
		byID[id] = fwtrading.VenueStatusFilled
	// ruleid: default-domain-status
	default:
		byID[id] = fwtrading.VenueStatusOpen
	}
}

func shortDeclaration(s string) fwtrading.VenueStatus {
	// ruleid: default-domain-status
	if status, ok := statuses[s]; ok {
		return status
	}
	status := fwtrading.VenueStatusOpen
	return status
}

func multiTarget(s string) (status VenueStatus, known bool) {
	switch s {
	case "filled":
		status, known = VenueStatusFilled, true
	// ruleid: default-domain-status
	default:
		known, status = false, VenueStatusOpen
	}
	return status, known
}

func literalStatus(s string) VenueStatus {
	switch s {
	case "filled":
		return VenueStatusFilled
	// todoruleid: default-domain-status
	default:
		return "open"
	}
}

func callArgument(s string) error {
	switch s {
	case "filled":
		return nil
	// ok: default-domain-status
	default:
		return fmt.Errorf("unknown status %q, want %v", s, VenueStatusFilled)
	}
}

func mapHitClosingComment(s string) VenueStatus {
	// ruleid: default-domain-status
	if status, ok := statuses[s]; ok {
		return status
	} // mapped
	return VenueStatusOpen
}

func parenInString(s string) (string, VenueStatus) {
	switch s {
	case "filled":
		return "filled", VenueStatusFilled
	// ruleid: default-domain-status
	default:
		return "unknown (venue)", VenueStatusOpen
	}
}

func indexedSlotFallback(raws []string) []VenueStatus {
	out := make([]VenueStatus, len(raws))
	for i, raw := range raws {
		// ruleid: default-domain-status
		if status, ok := statuses[raw]; ok {
			out[i] = status
			continue
		}
		out[i] = VenueStatusOpen
	}
	return out
}

func guardThenRegister(r *registry, id string) error {
	// todook: default-domain-status
	if _, exists := r.states[id]; exists {
		return errAlreadyRegistered
	}
	r.states[id] = domain.OrderStatusAccepted
	return nil
}

func guardThenRegisterField(r *registry, o *order) error {
	// todook: default-domain-status
	if _, exists := r.orders[o.ID]; exists {
		return errAlreadyRegistered
	}
	o.Status = domain.OrderStatusAccepted
	r.orders[o.ID] = o
	return nil
}

func guardThenRegisterLocal(r *registry, id string) error {
	// todook: default-domain-status
	if _, exists := r.states[id]; exists {
		return errAlreadyRegistered
	}
	state := domain.OrderStatusAccepted
	r.states[id] = state
	return nil
}

func commentAfterTuple(s string) (VenueStatus, bool) {
	switch s {
	case "filled":
		return VenueStatusFilled, true
	// ok: default-domain-status
	default:
		return "", false // unmapped, VenueStatusOpen would lie
	}
}

func commaInString(s string) (string, error) {
	switch s {
	case "ok":
		return "ok", nil
	// ok: default-domain-status
	default:
		return "unknown, StateError", errUnknown
	}
}

func unknownIsAnError(s string) (VenueStatus, bool) {
	switch s {
	case "filled":
		return VenueStatusFilled, true
	// ok: default-domain-status
	default:
		return "", false
	}
}

func unknownMapIsAnError(s string) (VenueStatus, error) {
	status, ok := statuses[s]
	// ok: default-domain-status
	if !ok {
		return "", fmt.Errorf("unknown status %q", s)
	}
	return status, nil
}

func classifier(a AuditAction) AuditCategory {
	switch a {
	case AuditActionSubmitOrder:
		return AuditCategoryTrading
	// ok: default-domain-status
	default:
		return AuditCategoryControl
	}
}

func passthrough(asset string) string {
	switch asset {
	case "XBT":
		return "BTC"
	// ok: default-domain-status
	default:
		return asset
	}
}

func callNotConstant(category string) []AuditAction {
	actions, known := AuditActionsForCategory(category)
	// ok: default-domain-status
	if !known {
		return AuditActionsByCategory(AuditCategoryControl)
	}
	return actions
}

func booleanBranch(blocked bool) AuditAction {
	action := AuditActionBlockGroup
	// ok: default-domain-status
	if !blocked {
		action = AuditActionUnblockGroup
	}
	return action
}

func bareHitFlag(ok bool) State {
	// ok: default-domain-status
	if ok {
		return StateOK
	}
	return StateError
}

func absenceIsAState(id string) string {
	status, ok := runtimeStatuses[id]
	// ok: default-domain-status
	if !ok {
		return "pending"
	}
	return status
}

func nestedClosure(kind string) error {
	switch kind {
	case "hold":
		return nil
	// ok: default-domain-status
	default:
		terminal := func(filled bool) VenueStatus {
			if filled {
				return VenueStatusFilled
			}
			return VenueStatusCancelled
		}
		return record(terminal)
	}
}
