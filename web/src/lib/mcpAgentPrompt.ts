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

import type { McpCommand } from "@/api/types";

/**
 * Build a plain-text instruction block for an AI agent describing how to use
 * the Pit Officer MCP server. Pass the live command catalogue; the function
 * lists all commands regardless of enabled state, since the text is pasted
 * once and does not automatically re-sync with operator access toggles.
 */
export function buildMcpAgentPrompt(commands: McpCommand[]): string {
  const lines: string[] = [
    "You have access to the Pit Officer MCP server - a pre-trade risk and",
    "compliance control plane for trading operations.",
    "",
    "Transport: streamable HTTP at the /mcp endpoint of this service.",
    "Alternatively available as `pit-officer mcp` over stdio.",
    "",
    "Principles:",
    "- Least-privilege surface. The server carries no secrets or credentials.",
    "- Most tools are read-only. Treat any mutating tool with extra care.",
    "- Any trading action or mutation must go through pre-trade checks and",
    "  approval tokens. Never bypass the pre-trade step.",
    "",
    "Commands (each is subject to operator access control):",
  ];

  for (const cmd of commands) {
    const desc = cmd.agentDescription || cmd.title || cmd.name;
    lines.push(`- ${cmd.name}: ${desc}`);
  }

  lines.push("");
  lines.push(
    "The operator controls which commands are enabled, and this can change at",
    "any time without notice. If a tool call returns a notice that the command",
    "is disabled in the panel, do NOT retry it — ask your orchestrator to",
    "extend or change your prompt/instructions accordingly (or to have the",
    "operator enable the command in Pit Officer).",
  );

  return lines.join("\n");
}
