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

// Pure builders for the per-platform "Connect your agent" artifacts. Every
// string here is agent-facing copy and stays English regardless of UI locale;
// each builder is parameterized by the live MCP endpoint URL. The generated
// SKILL.md / instruction text is a downloadable artifact, not a source file, so
// it carries no license header.
//
// The connect commands and configurations also take the agent token: the
// value in `token` access, `null` in `open` access, which has no token. They
// are the design's exact instructions, character for character, with the URL
// and the token substituted.

/**
 * Claude CLI command that registers the Officer MCP over streamable HTTP. It
 * is one line in both forms, so it pastes into any shell, PowerShell and cmd
 * included.
 */
export function claudeMcpAddCommand(url: string, token: string | null): string {
  const add = `claude mcp add --transport http pit-officer ${url}`;
  if (token === null) {
    return add;
  }
  return `${add} --header "Authorization: Bearer ${token}"`;
}

/**
 * Claude Desktop config snippet: an `mcpServers` entry that runs the
 * `mcp-remote` bridge, which passes the token from its `env` as a header.
 */
export function claudeDesktopConfig(url: string, token: string | null): string {
  if (token === null) {
    return `{
  "mcpServers": {
    "pit-officer": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote", "${url}"
      ]
    }
  }
}`;
  }
  return `{
  "mcpServers": {
    "pit-officer": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote", "${url}",
        "--header", "Authorization:\${PIT_OFFICER_AUTH}"
      ],
      "env": { "PIT_OFFICER_AUTH": "Bearer ${token}" }
    }
  }
}`;
}

/** Codex `~/.codex/config.toml` entry for the Officer MCP. */
export function codexConfig(url: string, token: string | null): string {
  const entry = `[mcp_servers.pit-officer]
url = "${url}"`;
  if (token === null) {
    return entry;
  }
  return `${entry}
http_headers = { Authorization = "Bearer ${token}" }`;
}

/** Cursor `~/.cursor/mcp.json` entry for the Officer MCP. */
export function cursorConfig(url: string, token: string | null): string {
  if (token === null) {
    return `{
  "mcpServers": {
    "pit-officer": {
      "url": "${url}"
    }
  }
}`;
  }
  return `{
  "mcpServers": {
    "pit-officer": {
      "url": "${url}",
      "headers": { "Authorization": "Bearer ${token}" }
    }
  }
}`;
}

/** Self-contained Claude Agent Skill SKILL.md body for operating Officer. */
export function claudeSkillMarkdown(url: string): string {
  return `---
name: pit-officer
description: Operate Pit Officer, a pre-trade risk and compliance control plane for trading operations, through its Model Context Protocol (MCP) server. Use when checking accounts, limits, or audit history, dry-running orders against pre-trade risk, or working with order approvals and kill-switches.
---

# Pit Officer

Pit Officer is a pre-trade risk and compliance control plane for trading
operations. It exposes its capabilities as an MCP server so an agent can read
control-plane state and route trading actions through pre-trade checks.

## Connect

Connect to the Officer MCP server over streamable HTTP at:

    ${url}

## Discover the command surface

Do NOT assume a fixed set of commands. After connecting, call the MCP tool list
(\`tools/list\`) to discover the commands this deployment currently exposes, and
rely on each tool's self-described input schema. The available surface can
change at any time as the operator adjusts access.

## Operating rules

- Least-privilege surface: the server carries no secrets or credentials.
- Most tools are read-only. Treat any mutating tool with extra care.
- Any trading action or mutation must go through pre-trade checks and
  approval. Never bypass the pre-trade step.
- Mutating and protective commands may be disabled by the operator. If a tool
  call returns a notice that the command is disabled, do NOT retry it - report
  back so the operator can enable it or your instructions can be adjusted.
`;
}

/** Custom-GPT instruction text mirroring the Claude skill, with connect notes. */
export function chatgptInstructions(url: string): string {
  return `# Pit Officer

Pit Officer is a pre-trade risk and compliance control plane for trading
operations. You operate it through its Model Context Protocol (MCP) server.

## Connect

Connect to the Officer MCP server over streamable HTTP at:

    ${url}

In ChatGPT this is configured as an MCP connector pointing at the URL above.
Alternatively, build a ChatGPT Action from the Officer OpenAPI description
served at the \`/docs\` endpoint of the same service.

Note: hosted ChatGPT runs outside the operator's computer and reaches only a
public HTTPS address. Officer stays on the operator's computer and serves plain
HTTP, so hosted ChatGPT cannot connect to it.

## Discover the command surface

Do NOT assume a fixed set of commands. After connecting, discover the commands
this deployment currently exposes via the MCP tool list (\`tools/list\`) and rely
on each tool's self-described input schema. The available surface can change at
any time as the operator adjusts access.

## Operating rules

- Least-privilege surface: the server carries no secrets or credentials.
- Most tools are read-only. Treat any mutating tool with extra care.
- Any trading action or mutation must go through pre-trade checks and
  approval. Never bypass the pre-trade step.
- Mutating and protective commands may be disabled by the operator. If a tool
  call returns a notice that the command is disabled, do NOT retry it - report
  back so the operator can enable it or your instructions can be adjusted.
`;
}
