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

import type { ComponentProps } from "react";

import { TextField } from "@/components/TextField";

type PasswordField = Pick<
  ComponentProps<typeof TextField>,
  "ref" | "id" | "label" | "value" | "onChange" | "error"
>;

/** A controlled new-password pair; the form owns validation and its timing. */
export function PasswordPairFields({
  password,
  repeat,
  disabled,
  revealed = false,
  verbatim = false,
}: {
  password: PasswordField;
  repeat: PasswordField;
  disabled: boolean;
  revealed?: boolean;
  verbatim?: boolean;
}) {
  const type = revealed ? "text" : "password";
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <TextField
        {...password}
        type={type}
        autoComplete="new-password"
        verbatim={verbatim}
        disabled={disabled}
      />
      <TextField
        {...repeat}
        type={type}
        autoComplete="new-password"
        verbatim={verbatim}
        disabled={disabled}
      />
    </div>
  );
}
