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

import { useState, type ComponentProps } from "react";

import { RevealButton } from "@/components/RevealButton";
import { TextField } from "@/components/TextField";

/**
 * A labelled, masked field for a secret the operator types in, with a
 * Show/Hide toggle. The value is taken verbatim, so a shown secret never
 * reaches a spell checker.
 */
export function SecretInput(
  props: Omit<ComponentProps<typeof TextField>, "type" | "verbatim" | "action">,
) {
  const [shown, setShown] = useState(false);
  return (
    <TextField
      {...props}
      type={shown ? "text" : "password"}
      verbatim
      action={
        <RevealButton
          shown={shown}
          onToggle={() => setShown(!shown)}
          disabled={props.disabled}
          variant="ghost"
        />
      }
    />
  );
}
