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

export type FilterValueType = "text" | "number" | "time";

export interface OperatorOption {
  value: string;
  label: string;
  sign?: string;
}

/** Operator sets keyed by value type. */
export const OPERATORS: Record<FilterValueType, OperatorOption[]> = {
  text: [
    { value: "contains", label: "Contains" },
    { value: "starts_with", label: "Starts with" },
    { value: "ends_with", label: "Ends with" },
    { value: "exact", label: "Exact" },
  ],
  number: [
    { value: "eq", label: "Equals", sign: "=" },
    { value: "neq", label: "Not equal", sign: "≠" },
    { value: "gt", label: "Greater than", sign: ">" },
    { value: "lt", label: "Less than", sign: "<" },
    { value: "gte", label: "At least", sign: "≥" },
    { value: "lte", label: "At most", sign: "≤" },
    { value: "between", label: "Between" },
  ],
  time: [
    { value: "after", label: "After" },
    { value: "before", label: "Before" },
    { value: "between", label: "Between" },
  ],
};
