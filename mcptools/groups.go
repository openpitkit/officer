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

package mcptools

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// The group tools map their JSON arguments 1:1 onto the account-group calls
// REST makes and leave every value rule, the reserved default group and the
// single-account refusal included, to the core.

const (
	createGroupToolName = "create_group"
	updateGroupToolName = "update_group"
	deleteGroupToolName = "delete_group"
)

const (
	createGroupToolDescription = "Create an account group with a code, an " +
		"optional title, an optional realized P&L currency and optional notes. " +
		"A member account without its own currency inherits the group's, then " +
		"the default group's; without a currency the group tier stays unset. " +
		"The code - is reserved for the default group and refused with " +
		"validation. Mutates Officer control-plane config; protected and " +
		"disabled by default."
	updateGroupToolDescription = "Change an account group's code and title; " +
		"the title replaces the stored one and an empty title clears it. The " +
		"default group cannot be changed, and the code - is reserved for it and " +
		"refused with validation. Mutates Officer control-plane config; " +
		"protected and disabled by default."
	deleteGroupToolDescription = "Delete an account group. Its member accounts " +
		"leave the group and keep their trading and compliance history, and a " +
		"block the group carries no longer applies to them; the delete is " +
		"refused with currency_change_blocked, even with force, when " +
		"leaving would change the effective currency of a member carrying " +
		"protected state. A group with a group-scoped Spot Funds P&L bound is " +
		"refused with has_dependents unless force deletes the bound with it. " +
		"The group's trading access grants are deleted with it; its former " +
		"members keep only their own. The default group cannot be deleted. " +
		"Mutates Officer control-plane config; protected and disabled by default."
)

type createGroupInput struct {
	Code     string `json:"code" jsonschema:"Group code; - is reserved for the default group"`
	Title    string `json:"title,omitempty" jsonschema:"Group title; omitted leaves the group untitled"`
	Currency string `json:"currency,omitempty" jsonschema:"Realized P&L currency asset code; omitted leaves the group tier unset, so a member account without its own currency inherits the default group's"`
	Notes    string `json:"notes,omitempty" jsonschema:"Free-form operator notes; omitted leaves none"`
}

type updateGroupInput struct {
	Group string `json:"group" jsonschema:"Current group code"`
	Code  string `json:"code" jsonschema:"New group code; equal to group to keep it; - is reserved for the default group"`
	Title string `json:"title" jsonschema:"New group title, replacing the stored one; empty clears it"`
}

type deleteGroupInput struct {
	Group string `json:"group" jsonschema:"Group code"`
	Force bool   `json:"force,omitempty" jsonschema:"Also delete the group's dependent rows, its group-scoped Spot Funds P&L bounds; without it a group with dependents is refused with has_dependents. A delete that would change the effective currency of a member carrying protected state is refused even with force"`
}

// groupOutput is the REST group body, {"group": {...}}.
type groupOutput struct {
	Group httpx.GroupDTO `json:"group"`
}

func createGroupHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[createGroupInput, groupOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in createGroupInput,
	) (string, groupOutput, error) {
		group, err := src.CreateGroup(ctx, domain.AccountGroup{
			Code:     in.Code,
			Title:    in.Title,
			Currency: in.Currency,
			Notes:    in.Notes,
		})
		if err != nil {
			return "", groupOutput{}, toolError(err)
		}
		out := groupOutput{Group: httpx.ToGroupDTO(group)}
		return fmt.Sprintf("group %s created", group.Code), out, nil
	}
}

func updateGroupHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[updateGroupInput, groupOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in updateGroupInput,
	) (string, groupOutput, error) {
		group, err := src.UpdateGroup(ctx, in.Group, domain.AccountGroup{
			Code:  in.Code,
			Title: in.Title,
		})
		if err != nil {
			return "", groupOutput{}, toolError(err)
		}
		out := groupOutput{Group: httpx.ToGroupDTO(group)}
		return fmt.Sprintf("group %s updated", group.Code), out, nil
	}
}

func deleteGroupHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[deleteGroupInput, deleteGroupInput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in deleteGroupInput,
	) (string, deleteGroupInput, error) {
		if err := src.DeleteGroup(ctx, in.Group, in.Force); err != nil {
			return "", deleteGroupInput{}, toolError(err)
		}
		return fmt.Sprintf("group %s deleted", in.Group), in, nil
	}
}
