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

/**
 * Build a plain-text orientation block for an AI agent connecting to the Pit
 * Officer MCP server. The text is intentionally evergreen: it does not
 * enumerate commands, since MCP self-describes its tools. The agent discovers
 * the available command surface via the MCP tool list (`tools/list`), so the
 * prompt never drifts from the operator's current access toggles.
 *
 * The prompt is agent-facing copy, so it stays English in every UI locale, as
 * the artifacts in `agentArtifacts.ts` do.
 */
export function buildMcpAgentPrompt(): string {
  return `You have access to the Pit Officer MCP server - a pre-trade risk and
compliance control plane for trading operations.

Transport: streamable HTTP at the /mcp endpoint of this service.
Alternatively available as \`pit-officer mcp\` over stdio.

Principles:
- Least-privilege surface. The server carries no secrets or credentials.
- Most tools are read-only. Treat any mutating tool with extra care.
- Any trading action or mutation must go through pre-trade checks and
  approval. Never bypass the pre-trade step.

Discover the available commands at runtime: call the MCP tool list
(\`tools/list\`) after connecting and rely on each tool's self-described
input schema. Do not assume a fixed set of commands - the surface is
self-describing and can change as the operator adjusts access.

The operator controls which commands are enabled, and this can change at
any time without notice. If a tool call returns a notice that the command
is disabled in the panel, do NOT retry it - ask your orchestrator to
extend or change your prompt/instructions accordingly (or to have the
operator enable the command in Pit Officer).`;
}
