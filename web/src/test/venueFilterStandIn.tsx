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

import {
  VENUE_FILTER_ANY,
  VENUE_FILTER_NONE,
  venueConnectionFilterValue,
  type VenueFilterProps,
} from "@/components/data";

/**
 * Page-test stand-in for the library VenueFilter, which has its own test: it
 * shows what a page passes and drives onChange with one button per choice.
 */
export function VenueFilterStandIn({
  label,
  value,
  connections,
  allLabel,
  anyLabel,
  noneLabel,
  width,
  onChange,
}: VenueFilterProps) {
  return (
    <fieldset aria-label={label} style={{ width }}>
      <legend>{label}</legend>
      <output aria-label="Applied venue draft">{value}</output>
      <button type="button" onClick={() => onChange("")}>
        {allLabel}
      </button>
      <button type="button" onClick={() => onChange(VENUE_FILTER_ANY)}>
        {anyLabel}
      </button>
      <button type="button" onClick={() => onChange(VENUE_FILTER_NONE)}>
        {noneLabel}
      </button>
      {connections.map((connection) => (
        <button
          key={connection.id}
          type="button"
          onClick={() => onChange(venueConnectionFilterValue(connection.id))}
        >
          {connection.label}
        </button>
      ))}
    </fieldset>
  );
}
