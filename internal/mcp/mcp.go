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

// Package mcp preserves the application's historical MCP import path while the
// reusable MCP server machinery lives in the framework module.
package mcp

import (
	"context"
	"net/http"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/mcp/tools"
)

type Source = frameworkmcp.Source
type Status = frameworkmcp.Status
type NodeHealth = frameworkmcp.NodeHealth
type EngineHealth = frameworkmcp.EngineHealth
type StoreHealth = frameworkmcp.StoreHealth
type SubmitOrderTokenResult = frameworkmcp.SubmitOrderTokenResult
type SubmitDropCopyOrderResult = frameworkmcp.SubmitDropCopyOrderResult
type Attestation = frameworkmcp.Attestation
type VersionSource = frameworkmcp.VersionSource
type ToolRegistry = frameworkmcp.ToolRegistry
type ToolDescriptor = frameworkmcp.ToolDescriptor
type RegisterDeps = frameworkmcp.RegisterDeps
type Authorizer = httpx.Authorizer

// NewToolRegistry builds the Pit Officer MCP registry.
func NewToolRegistry(src Source) *ToolRegistry {
	reg := frameworkmcp.NewToolRegistry()
	tools.RegisterTools(reg, src)
	return reg
}

// NewServer builds the Pit Officer MCP server with the allow-all
// authorizer used by the historical implementation.
func NewServer(src Source, version VersionSource) (*sdkmcp.Server, error) {
	return NewServerWithAuthorizer(src, version, httpx.AllowAll{})
}

// NewServerWithAuthorizer builds the Pit Officer MCP server with an injected
// framework authorizer.
func NewServerWithAuthorizer(
	src Source,
	version VersionSource,
	authorizer Authorizer,
) (*sdkmcp.Server, error) {
	return NewServerWithRegistry(NewToolRegistry(src), src, version, authorizer)
}

// NewServerWithRegistry builds an MCP server from a caller-provided registry.
func NewServerWithRegistry(
	reg *ToolRegistry,
	src Source,
	version VersionSource,
	authorizer Authorizer,
) (*sdkmcp.Server, error) {
	return frameworkmcp.Build(reg, src, version, authorizer)
}

// RunStdio builds the Pit Officer MCP server and serves it over stdio.
func RunStdio(ctx context.Context, src Source, version VersionSource) error {
	return RunStdioWithAuthorizer(ctx, src, version, httpx.AllowAll{})
}

// RunStdioWithAuthorizer serves the Pit Officer MCP server over stdio with an
// injected framework authorizer.
func RunStdioWithAuthorizer(
	ctx context.Context,
	src Source,
	version VersionSource,
	authorizer Authorizer,
) error {
	return RunStdioWithRegistry(ctx, NewToolRegistry(src), src, version, authorizer)
}

// RunStdioWithRegistry serves a registry-provided MCP server over stdio.
func RunStdioWithRegistry(
	ctx context.Context,
	reg *ToolRegistry,
	src Source,
	version VersionSource,
	authorizer Authorizer,
) error {
	return frameworkmcp.RunStdio(ctx, reg, src, version, authorizer)
}

// Handler builds the streamable-HTTP MCP handler for the built-in tool set.
func Handler(src Source, version VersionSource) (http.Handler, error) {
	return HandlerWithAuthorizer(src, version, httpx.AllowAll{})
}

// HandlerWithAuthorizer builds the streamable-HTTP MCP handler with an injected
// framework authorizer.
func HandlerWithAuthorizer(
	src Source,
	version VersionSource,
	authorizer Authorizer,
) (http.Handler, error) {
	return HandlerWithRegistry(NewToolRegistry(src), src, version, authorizer)
}

// HandlerWithRegistry builds the streamable-HTTP MCP handler from a registry.
func HandlerWithRegistry(
	reg *ToolRegistry,
	src Source,
	version VersionSource,
	authorizer Authorizer,
) (http.Handler, error) {
	return frameworkmcp.Handler(reg, src, version, authorizer)
}
