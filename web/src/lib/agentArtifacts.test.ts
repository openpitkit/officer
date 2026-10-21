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

import { describe, expect, it } from "vitest";

import {
  claudeDesktopConfig,
  claudeMcpAddCommand,
  codexConfig,
  cursorConfig,
} from "@/lib/agentArtifacts";

const URL = "http://127.0.0.1:8787/mcp";
const TOKEN = "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w";

// The access design's exact instructions for the agent token in an MCP client,
// with the URL and the token substituted; the `open` forms drop the token
// lines.
describe("agentArtifacts", () => {
  it("builds the Claude CLI command as one line with the token header only in token access", () => {
    expect(claudeMcpAddCommand(URL, null)).toBe(
      "claude mcp add --transport http pit-officer http://127.0.0.1:8787/mcp",
    );
    expect(claudeMcpAddCommand(URL, TOKEN)).toBe(
      "claude mcp add --transport http pit-officer http://127.0.0.1:8787/mcp" +
        ' --header "Authorization: Bearer pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w"',
    );
  });

  it("builds the Claude Desktop entry through mcp-remote, with the token in env only in token access", () => {
    expect(claudeDesktopConfig(URL, null)).toBe(
      [
        "{",
        '  "mcpServers": {',
        '    "pit-officer": {',
        '      "command": "npx",',
        '      "args": [',
        '        "-y", "mcp-remote", "http://127.0.0.1:8787/mcp"',
        "      ]",
        "    }",
        "  }",
        "}",
      ].join("\n"),
    );
    expect(claudeDesktopConfig(URL, TOKEN)).toBe(
      [
        "{",
        '  "mcpServers": {',
        '    "pit-officer": {',
        '      "command": "npx",',
        '      "args": [',
        '        "-y", "mcp-remote", "http://127.0.0.1:8787/mcp",',
        '        "--header", "Authorization:${PIT_OFFICER_AUTH}"',
        "      ],",
        '      "env": { "PIT_OFFICER_AUTH": "Bearer pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w" }',
        "    }",
        "  }",
        "}",
      ].join("\n"),
    );
  });

  it("builds the Codex entry with http_headers only in token access", () => {
    expect(codexConfig(URL, null)).toBe(
      ["[mcp_servers.pit-officer]", 'url = "http://127.0.0.1:8787/mcp"'].join(
        "\n",
      ),
    );
    expect(codexConfig(URL, TOKEN)).toBe(
      [
        "[mcp_servers.pit-officer]",
        'url = "http://127.0.0.1:8787/mcp"',
        'http_headers = { Authorization = "Bearer pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w" }',
      ].join("\n"),
    );
  });

  it("builds the Cursor entry with headers only in token access", () => {
    expect(cursorConfig(URL, null)).toBe(
      [
        "{",
        '  "mcpServers": {',
        '    "pit-officer": {',
        '      "url": "http://127.0.0.1:8787/mcp"',
        "    }",
        "  }",
        "}",
      ].join("\n"),
    );
    expect(cursorConfig(URL, TOKEN)).toBe(
      [
        "{",
        '  "mcpServers": {',
        '    "pit-officer": {',
        '      "url": "http://127.0.0.1:8787/mcp",',
        '      "headers": { "Authorization": "Bearer pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w" }',
        "    }",
        "  }",
        "}",
      ].join("\n"),
    );
  });

  it("produces valid JSON for both Claude Desktop and Cursor entries", () => {
    for (const token of [null, TOKEN]) {
      expect(() => {
        JSON.parse(claudeDesktopConfig(URL, token));
      }).not.toThrow();
      expect(() => {
        JSON.parse(cursorConfig(URL, token));
      }).not.toThrow();
    }
  });
});
