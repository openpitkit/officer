-- Copyright The Pit Project Owners. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.
--
-- Please see https://openpit.dev and the OWNERS file for details.

-- Per-command MCP access control. One row per command the operator has
-- explicitly toggled away from its catalogue default; commands without a row
-- resolve to their default in the catalogue. `enabled` is a 0/1 flag.
CREATE TABLE mcp_access (
    command TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL
);
