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

import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  AccountPnlResetSchedule,
  ResetAccountPnlConfirm,
} from "@/components/AccountPnlReset";
import {
  fetchResetSchedule,
  recoverResetSchedule,
  saveResetSchedule,
  resetRealizedAccountPnl,
} from "@/api/accountPnlReset";
import { accountRow, renderWithApi } from "@/test/apiClient";
import i18n from "@/i18n";
vi.mock("@/api/accountPnlReset", () => ({
  fetchResetSchedule: vi.fn(),
  recoverResetSchedule: vi.fn(),
  saveResetSchedule: vi.fn(),
  resetRealizedAccountPnl: vi.fn(),
}));
beforeEach(async () => {
  vi.resetAllMocks();
  await i18n.changeLanguage("en");
  vi.mocked(fetchResetSchedule).mockResolvedValue({
    job: null,
    runtimeError: "",
  });
});
describe("account P&L force reset", () => {
  it("disables paused fields and edits the end datetime explicitly in UTC", async () => {
    const user = userEvent.setup();
    vi.mocked(fetchResetSchedule).mockResolvedValue({
      runtimeError: "",
      job: {
        id: "existing",
        state: "paused",
        revision: 12,
        completedRuns: 2,
        spec: {
          schedule: { time: "09:00", timezone: "Asia/Tokyo" },
          termination: { mode: "until", until: "2030-01-01T00:00:00Z" },
        },
        next: { at: "2029-12-31T00:00:00Z" },
      },
    });
    vi.mocked(saveResetSchedule).mockResolvedValue({
      job: null,
      runtimeError: "",
    });
    renderWithApi(<AccountPnlResetSchedule />);
    const field = await screen.findByLabelText("End date and time (UTC)");
    expect(field).toHaveAttribute("type", "datetime-local");
    expect(field).toBeDisabled();
    expect(screen.getByLabelText("Daily reset time")).toBeDisabled();
    expect(screen.getByLabelText("Time zone")).toBeDisabled();
    expect(screen.getByLabelText("Repetition ends")).toBeDisabled();
    await user.click(
      screen.getByRole("checkbox", { name: "Enable daily reset" }),
    );
    fireEvent.change(field, { target: { value: "" } });
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText("Select a valid end date and time in UTC."),
    ).toBeVisible();
    expect(saveResetSchedule).not.toHaveBeenCalled();
    fireEvent.change(field, { target: { value: "2030-01-01T00:00" } });
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(saveResetSchedule).toHaveBeenCalledWith(expect.anything(), {
        enabled: true,
        time: "09:00",
        timezone: "Asia/Tokyo",
        termination: { mode: "until", until: "2030-01-01T00:00:00.000Z" },
        revision: 12,
      }),
    );
  });
  it("requires confirmation for a halted account and preserves failure for review", async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    const onClose = vi.fn();
    vi.mocked(resetRealizedAccountPnl)
      .mockRejectedValueOnce(new Error("store unavailable"))
      .mockResolvedValueOnce();
    renderWithApi(
      <ResetAccountPnlConfirm
        account={accountRow("halted", {
          pnl: "",
          pnlHaltReason: "missing_fx",
          blocked: true,
        })}
        onDone={onDone}
        onClose={onClose}
      />,
    );
    expect(screen.getByRole("alertdialog")).toHaveTextContent(
      "Reset realized P&L for halted?",
    );
    expect(screen.getByRole("alertdialog")).toHaveTextContent(
      "Accounts already blocked stay blocked",
    );
    expect(resetRealizedAccountPnl).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Reset to 0" }));
    expect(await screen.findByText("store unavailable")).toBeVisible();
    expect(onDone).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Reset to 0" }));
    await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(resetRealizedAccountPnl).toHaveBeenLastCalledWith(
      expect.anything(),
      "halted",
    );
  });
  it("leaves new daily time and timezone empty and saves one explicit rule", async () => {
    const user = userEvent.setup();
    vi.mocked(saveResetSchedule).mockResolvedValue({
      job: null,
      runtimeError: "",
    });
    renderWithApi(<AccountPnlResetSchedule />);
    expect(
      await screen.findByText("Not configured", { exact: false }),
    ).toBeVisible();
    const time = await screen.findByLabelText("Daily reset time");
    expect(time).toHaveValue("");
    expect(time).toHaveAttribute("type", "time");
    expect(
      screen.getByRole("checkbox", { name: "Enable daily reset" }),
    ).not.toBeChecked();
    expect(time).toBeDisabled();
    expect(screen.getByLabelText("Time zone")).toBeDisabled();
    await user.click(
      screen.getByRole("checkbox", { name: "Enable daily reset" }),
    );
    expect(screen.getByLabelText("Time zone")).toHaveTextContent(
      "Select a time zone from the list.",
    );
    fireEvent.change(time, { target: { value: "09:30" } });
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(saveResetSchedule).not.toHaveBeenCalled();
    await user.click(screen.getByLabelText("Time zone"));
    await user.type(screen.getByRole("searchbox"), "Europe/London");
    await user.click(
      screen.getByRole("menuitemradio", { name: "Europe/London" }),
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(saveResetSchedule).toHaveBeenCalledWith(expect.anything(), {
        enabled: true,
        time: "09:30",
        timezone: "Europe/London",
        termination: { mode: "forever" },
        revision: 0,
      }),
    );
  });
  it.each(["ready", "paused", "completed"])(
    "reads the checkbox from the saved %s schedule",
    async (state) => {
      vi.mocked(fetchResetSchedule).mockResolvedValue({
        runtimeError: "",
        job: {
          id: "saved",
          state,
          revision: 4,
          completedRuns: 0,
          spec: {
            schedule: { time: "09:00", timezone: "Europe/London" },
            termination: { mode: "forever" },
          },
          next: { at: "2030-01-01T09:00:00Z" },
        },
      });
      renderWithApi(<AccountPnlResetSchedule />);
      const checkbox = await screen.findByRole("checkbox", {
        name: "Enable daily reset",
      });
      if (state === "ready") {
        expect(checkbox).toBeChecked();
        expect(screen.getByLabelText("Time zone")).toBeEnabled();
      } else {
        expect(checkbox).not.toBeChecked();
        expect(screen.getByLabelText("Time zone")).toBeDisabled();
      }
      expect(screen.getByLabelText("Time zone")).toHaveTextContent(
        "Europe/London",
      );
    },
  );
  it("never silently retries an interrupted occurrence", async () => {
    const user = userEvent.setup();
    vi.mocked(fetchResetSchedule).mockResolvedValue({
      runtimeError: "",
      job: {
        id: "one-job",
        revision: 8,
        state: "interrupted",
        completedRuns: 2,
        spec: {
          schedule: { time: "09:00", timezone: "UTC" },
          termination: { mode: "count", maxRuns: 100 },
        },
        next: { at: "2030-01-01T09:00:00Z" },
        error: "effects uncertain",
        last: {
          finishedAt: "2030-01-01T09:01:00Z",
          outcome: "interrupted",
          error: "effects uncertain",
          result: {
            skippedReason: "",
            accounts: [{ account: "desk", error: "account unavailable" }],
          },
        },
      },
    });
    vi.mocked(recoverResetSchedule).mockResolvedValue({
      job: null,
      runtimeError: "",
    });
    renderWithApi(<AccountPnlResetSchedule />);
    expect(
      (await screen.findAllByText("Interrupted", { exact: false }))[0],
    ).toBeVisible();
    expect(screen.getByText("desk: account unavailable")).toBeVisible();
    expect(recoverResetSchedule).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Retry occurrence" }));
    await waitFor(() =>
      expect(recoverResetSchedule).toHaveBeenCalledWith(
        expect.anything(),
        8,
        "retry",
      ),
    );
  });
});
