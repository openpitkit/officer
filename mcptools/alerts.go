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
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

type alertIDInput struct {
	ID string `json:"id" jsonschema:"Stable alert ID"`
}
type alertReadInput struct {
	ID   string `json:"id"`
	Read *bool  `json:"read"`
}
type alertActionInput struct {
	ID     string          `json:"id"`
	Action string          `json:"action"`
	Input  json.RawMessage `json:"input"`
}

// RegisterAlertTools adds authenticated guarded tools over the composition's
// alert service, without extending the general MCP Source interface.
func RegisterAlertTools(reg *frameworkmcp.ToolRegistry, s *alerts.Service) {
	registerTool(reg, descriptor("list_alerts", "List alerts", "List unresolved alerts with your personal read state. Does not mark alerts read.", "Read unresolved alerts.", false, false, true, true), func(frameworkmcp.Source) frameworkmcp.ToolBody[struct{}, alerts.List] {
		return func(ctx context.Context, _ *sdkmcp.ServerSession, _ struct{}) (string, alerts.List, error) {
			out, err := s.List(ctx)
			if err != nil {
				return "", alerts.List{}, toolError(err)
			}
			return "Unresolved alerts", out, nil
		}
	})
	registerTool(reg, descriptor("get_alert", "Get alert", "Read one unresolved alert, its decisions and delivery outcomes.", "Read one alert.", false, false, true, true), func(frameworkmcp.Source) frameworkmcp.ToolBody[alertIDInput, alerts.Alert] {
		return func(ctx context.Context, _ *sdkmcp.ServerSession, in alertIDInput) (string, alerts.Alert, error) {
			out, err := s.Detail(ctx, in.ID)
			if err != nil {
				return "", alerts.Alert{}, toolError(err)
			}
			return "Alert", out, nil
		}
	})
	registerTool(reg, descriptor("set_alert_read", "Set alert read state", "Set your personal read or unread mark. Does not resolve the alert.", "Change your personal mark.", true, false, true, false), func(frameworkmcp.Source) frameworkmcp.ToolBody[alertReadInput, struct{}] {
		return func(ctx context.Context, _ *sdkmcp.ServerSession, in alertReadInput) (string, struct{}, error) {
			if in.Read == nil {
				return "", struct{}{}, toolError(fmt.Errorf("read is required: %w", domain.ErrInvalid))
			}
			err := s.SetRead(ctx, in.ID, *in.Read)
			if err != nil {
				return "", struct{}{}, toolError(err)
			}
			return "Read state saved", struct{}{}, nil
		}
	})
	registerTool(reg, descriptor("resolve_alert", "Resolve alert", "Perform a registered type-specific alert decision. Success records its result and removes the open alert. Invalid input or action failure keeps it open.", "Resolve an alert with a registered decision.", true, false, true, false), func(frameworkmcp.Source) frameworkmcp.ToolBody[alertActionInput, alerts.Resolution] {
		return func(ctx context.Context, _ *sdkmcp.ServerSession, in alertActionInput) (string, alerts.Resolution, error) {
			out, err := s.Resolve(ctx, in.ID, in.Action, in.Input)
			if err != nil {
				return "", alerts.Resolution{}, toolError(err)
			}
			return "Alert resolved", out, nil
		}
	})
}
