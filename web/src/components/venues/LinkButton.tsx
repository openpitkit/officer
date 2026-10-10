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
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";

/** The one way to act on a hint: a router link when the fix is on another
 *  page, a button when the caller can reach it itself. `to` is an in-app
 *  router path followed client-side, so the link form needs a router above
 *  it. */
export type LinkButtonProps = { children: ReactNode } & (
  | { to: string; onClick?: never }
  | { onClick: () => void; to?: never }
);

/** Small ghost button styled as an accent link. */
export function LinkButton(props: LinkButtonProps) {
  const className = "text-accent underline underline-offset-4";
  return props.to !== undefined ? (
    <Button asChild variant="ghost" size="sm" className={className}>
      <Link to={props.to}>{props.children}</Link>
    </Button>
  ) : (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className={className}
      onClick={props.onClick}
    >
      {props.children}
    </Button>
  );
}
