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

import type { ReactNode } from "react";

import { Input } from "@/components/ui/input";

/** The state of an acknowledgement a caller asks for before it acts. */
export type AcknowledgementState = {
  acknowledged: boolean;
  onChange: (acknowledged: boolean) => void;
  disabled: boolean;
};

/**
 * The checkbox that ends a danger notice when a caller asks the operator to
 * confirm they understand it; `children` is its label.
 */
export function Acknowledgement({
  acknowledged,
  onChange,
  disabled,
  children,
}: AcknowledgementState & { children: ReactNode }) {
  return (
    <label className="flex items-center gap-2 pt-1 text-sm font-medium text-text">
      <Input
        type="checkbox"
        className="h-4 w-4"
        checked={acknowledged}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      {children}
    </label>
  );
}
