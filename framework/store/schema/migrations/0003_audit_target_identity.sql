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
-- Please see https://officer.openpit.dev and the OWNERS file for details.

-- Audit identity snapshots outlive dictionary deletes and are never foreign
-- keys. Zero explicitly denotes an unavailable identity. No historical ID is
-- reconstructed from a mutable code.
ALTER TABLE audit ADD COLUMN account_identity BIGINT NOT NULL DEFAULT 0;
ALTER TABLE audit ADD COLUMN group_identity BIGINT NOT NULL DEFAULT 0;
