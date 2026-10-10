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

import { FieldLabel } from "./FilterBar";
import { FilterOperatorSelect } from "./FilterOperators";
import {
  normalizeVenueFilterValue,
  VENUE_FILTER_ANY,
  VENUE_FILTER_NONE,
  venueConnectionFilterValue,
  venueFilterConnectionId,
} from "./venueFilterValue";

const ALL_VALUE = "_all";

/** Width in pixels that fits a "venue · mode · name" connection label. */
export const VENUE_FILTER_WIDTH = 320;

/** Caller-labelled choices in the REST venue filter encoding. */
export interface VenueFilterProps {
  /** Field label and accessible name of the select. */
  label: string;
  /** `""` and `all` mean all rows; otherwise `any`, `none`, or `connection:<id>`. */
  value: string;
  /** Selectable trading connections in display order. */
  connections: { id: string; label: string }[];
  /** Label for the unrestricted option. */
  allLabel: string;
  /** Label for rows linked to at least one connection. */
  anyLabel: string;
  /** Label for rows linked to no connection. */
  noneLabel: string;
  /** Receives the selected REST value, including empty for all rows. */
  onChange: (value: string) => void;
  /** Control width in pixels, using the library select default if omitted. */
  width?: number;
}

/** Venue filter field that preserves unknown connection IDs as raw labels. */
export function VenueFilter({
  label,
  value,
  connections,
  allLabel,
  anyLabel,
  noneLabel,
  onChange,
  width,
}: VenueFilterProps) {
  const options = [
    { value: ALL_VALUE, label: allLabel },
    { value: VENUE_FILTER_ANY, label: anyLabel },
    { value: VENUE_FILTER_NONE, label: noneLabel },
    ...connections.map((connection) => ({
      value: venueConnectionFilterValue(connection.id),
      label: connection.label,
    })),
  ];
  const normalizedValue = normalizeVenueFilterValue(value);
  const connectionId = venueFilterConnectionId(value);
  if (
    connectionId !== undefined &&
    !connections.some((connection) => connection.id === connectionId)
  ) {
    options.push({ value, label: connectionId });
  }

  return (
    <label className="grid gap-1.5">
      <FieldLabel>{label}</FieldLabel>
      <FilterOperatorSelect
        ariaLabel={label}
        options={options}
        value={normalizedValue === "" ? ALL_VALUE : normalizedValue}
        onChange={(next) => onChange(next === ALL_VALUE ? "" : next)}
        width={width}
      />
    </label>
  );
}
