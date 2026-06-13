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

import i18n from "@/i18n";

/**
 * Build a plain-text orientation block for an AI agent connecting to the Pit
 * Officer MCP server. The text is intentionally evergreen: it does not
 * enumerate commands, since MCP self-describes its tools. The agent discovers
 * the available command surface via the MCP tool list (`tools/list`), so the
 * prompt never drifts from the operator's current access toggles.
 *
 * The agent-facing copy stays English in every locale.
 */
export function buildMcpAgentPrompt(): string {
  return [
    i18n.t("mcp:prompt.intro"),
    "",
    i18n.t("mcp:prompt.discover"),
    "",
    i18n.t("mcp:prompt.footer"),
  ].join("\n");
}
