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

import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { AuthProvider } from "./AuthProvider";
import { useHasPermission } from "./auth-context";
import { RowActions, registerRowAction, unregisterRowAction } from "..";

function PermissionProbe() {
  const hasPermission = useHasPermission();
  return (
    <output aria-label="permission">
      {hasPermission("closed.permission") ? "allowed" : "denied"}
    </output>
  );
}

describe("AuthProvider", () => {
  afterEach(() => {
    unregisterRowAction("auth-gated-action");
    unregisterRowAction("auth-open-action");
  });

  it("allows every permission when no provider is mounted", () => {
    render(<PermissionProbe />);

    expect(screen.getByLabelText("permission").textContent).toBe("allowed");
  });

  it("hides gated row actions with a restrictive predicate", () => {
    registerRowAction<object, object>({
      id: "auth-gated-action",
      kind: "auth-smoke",
      order: 10,
      permission: "closed.permission",
      render: () => <button type="button">Gated action</button>,
    });
    registerRowAction<object, object>({
      id: "auth-open-action",
      kind: "auth-smoke",
      order: 20,
      render: () => <button type="button">Open action</button>,
    });

    render(
      <AuthProvider hasPermission={() => false}>
        <RowActions kind="auth-smoke" row={{}} ctx={{}} />
      </AuthProvider>,
    );

    expect(screen.queryByRole("button", { name: "Gated action" })).toBeNull();
    expect(screen.getByRole("button", { name: "Open action" })).not.toBeNull();
  });
});
