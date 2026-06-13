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

-- Operator-set manual mark price for bring-your-own (manual) instruments. The
-- operator sets it once; the connector manager pushes it into the engine as a
-- single quote at startup and on upsert. Exact TEXT decimal, never REAL; empty
-- means no manual price. Streaming providers ignore it.
ALTER TABLE market_data_instruments
    ADD COLUMN manual_price TEXT NOT NULL DEFAULT '';
