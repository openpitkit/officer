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
	hostToolID     = "customhost_host"
	replacedToolID = "health"
	hiddenToolID   = "get_limits"
	removedToolID  = "cancel"
)

type hostToolInput struct {
	Name string `json:"name,omitempty" jsonschema:"Optional subject name"`
}

type hostToolOutput struct {
	Message string `json:"message"`
}

func registerCustomHostTools(reg *mcp.ToolRegistry, src mcp.Source) {
	reg.Register(replacedToolDescriptor())
	reg.Unregister(removedToolID)
	registerHostTool(reg, src)
}

func registerHostTool(reg *mcp.ToolRegistry, _ mcp.Source) {
	descriptor := hostToolDescriptor()
	descriptor.Register = func(server *sdkmcp.Server, deps mcp.RegisterDeps) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{
			Name:        descriptor.Name,
			Description: descriptor.Description,
		}, mcp.Guard(descriptor, deps, hostToolHandler))
	}
	reg.Register(descriptor)
}

func hostToolDescriptor() mcp.ToolDescriptor {
	return mcp.ToolDescriptor{
		Name:             hostToolID,
		Title:            "Custom host tool",
		Description:      "Return a canned custom host response.",
		AgentDescription: "Return a canned custom host response.",
		Implemented:      true,
		DefaultEnabled:   false,
	}
}

func replacedToolDescriptor() mcp.ToolDescriptor {
	descriptor := mcp.ToolDescriptor{
		Name:             replacedToolID,
		Title:            "Custom host health replacement",
		Description:      "Replacement descriptor for the framework health tool id.",
		AgentDescription: "Replacement descriptor for the framework health tool id.",
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

func hostToolHandler(
	_ context.Context,
	_ *sdkmcp.ServerSession,
	in hostToolInput,
) (string, hostToolOutput, error) {
	name := in.Name
	if name == "" {
		name = "operator"
	}
	msg := "custom host response for " + name
	return msg, hostToolOutput{Message: msg}, nil
}

func replacedToolHandler(
	_ context.Context,
	_ *sdkmcp.ServerSession,
	_ struct{},
) (string, map[string]string, error) {
	return "custom host health replacement", map[string]string{"tool": "replaced"}, nil
}
