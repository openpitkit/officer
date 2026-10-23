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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import {
  FullScreenError,
  FullScreenLoading,
  LoadingStatus,
  TableSkeleton,
} from "@/components/PageStates";
import i18n from "@/i18n";

describe("TableSkeleton", () => {
  it("announces a stable busy loading status", () => {
    render(<TableSkeleton />);

    expect(
      screen.getByRole("status", { name: i18n.t("states.loading") }),
    ).toHaveAttribute("aria-busy", "true");
  });
});

describe("LoadingStatus", () => {
  it("announces a busy loading status", () => {
    render(<LoadingStatus />);

    expect(
      screen.getByRole("status", { name: i18n.t("states.loading") }),
    ).toHaveAttribute("aria-busy", "true");
  });
});

describe("FullScreenLoading", () => {
  it("announces a busy loading status", () => {
    render(<FullScreenLoading />);

    expect(
      screen.getByRole("status", { name: i18n.t("states.loading") }),
    ).toHaveAttribute("aria-busy", "true");
  });
});

describe("FullScreenError", () => {
  it("shows the title and the message and retries", async () => {
    const user = userEvent.setup();
    const onRetry = vi.fn();
    render(
      <FullScreenError
        title="Could not check the session"
        message="connection refused"
        onRetry={onRetry}
      />,
    );

    expect(screen.getByText("Could not check the session")).toBeInTheDocument();
    expect(screen.getByText("connection refused")).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: i18n.t("actions.retry") }),
    );
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("reads as the error state without a title", () => {
    render(<FullScreenError message="connection refused" onRetry={vi.fn()} />);

    expect(screen.getByText(i18n.t("states.loadError"))).toBeInTheDocument();
  });
});
