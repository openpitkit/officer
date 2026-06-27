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

package main

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/mcp"
)

const (
	privateToolID  = "closedref_private"
	replacedToolID = "health"
	hiddenToolID   = "get_limits"
	removedToolID  = "cancel"
)

type privateToolInput struct {
	Name string `json:"name,omitempty" jsonschema:"Optional subject name"`
}

type privateToolOutput struct {
	Message string `json:"message"`
}

func registerReferenceTools(reg *mcp.ToolRegistry, src mcp.Source) {
	reg.Register(replacedToolDescriptor())
	reg.Unregister(removedToolID)
	registerPrivateTool(reg, src)
}

func registerPrivateTool(reg *mcp.ToolRegistry, _ mcp.Source) {
	descriptor := privateToolDescriptor()
	descriptor.Register = func(server *sdkmcp.Server, deps mcp.RegisterDeps) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{
			Name:        descriptor.Name,
			Description: descriptor.Description,
		}, mcp.Guard(descriptor, deps, privateToolHandler))
	}
	reg.Register(descriptor)
}

func privateToolDescriptor() mcp.ToolDescriptor {
	return mcp.ToolDescriptor{
		Name:             privateToolID,
		Title:            "Private reference",
		Description:      "Return a canned private reference response.",
		AgentDescription: "Return a canned private reference response.",
		Implemented:      true,
		DefaultEnabled:   false,
	}
}

func replacedToolDescriptor() mcp.ToolDescriptor {
	descriptor := mcp.ToolDescriptor{
		Name:             replacedToolID,
		Title:            "Private health replacement",
		Description:      "Replacement descriptor for the open health tool id.",
		AgentDescription: "Replacement descriptor for the open health tool id.",
		Implemented:      true,
		DefaultEnabled:   true,
	}
	descriptor.Register = func(server *sdkmcp.Server, deps mcp.RegisterDeps) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{
			Name:        descriptor.Name,
			Description: descriptor.Description,
		}, mcp.Guard(descriptor, deps, replacedToolHandler))
	}
	return descriptor
}

func privateToolHandler(
	_ context.Context,
	_ *sdkmcp.ServerSession,
	in privateToolInput,
) (string, privateToolOutput, error) {
	name := in.Name
	if name == "" {
		name = "operator"
	}
	msg := "private reference response for " + name
	return msg, privateToolOutput{Message: msg}, nil
}

func replacedToolHandler(
	_ context.Context,
	_ *sdkmcp.ServerSession,
	_ struct{},
) (string, map[string]string, error) {
	return "closed health replacement", map[string]string{"tool": "replaced"}, nil
}
