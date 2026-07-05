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

import { useCallback } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { usePolling } from "@/api/usePolling";

function PollingProbe({
  fetchSpy,
  value,
}: {
  fetchSpy: (value: string) => void;
  value: string;
}) {
  const fetcher = useCallback(
    async () => {
      fetchSpy(value);
      return value;
    },
    [fetchSpy, value],
  );
  const { load } = usePolling(fetcher, 60000, value);
  return <span>{load.state === "ready" ? load.data : load.state}</span>;
}

function AbortProbe({ onAbort }: { onAbort: () => void }) {
  const fetcher = useCallback(
    (signal: AbortSignal) =>
      new Promise<string>(() => {
        signal.addEventListener("abort", onAbort);
      }),
    [onAbort],
  );
  usePolling(fetcher, 60000);
  return <span>mounted</span>;
}

describe("usePolling", () => {
  it("refetches immediately when the refresh key changes", async () => {
    const fetchSpy = vi.fn();
    const { rerender } = render(
      <PollingProbe fetchSpy={fetchSpy} value="first" />,
    );

    await waitFor(() => {
      expect(screen.getByText("first")).toBeInTheDocument();
    });
    expect(fetchSpy).toHaveBeenCalledTimes(1);

    rerender(<PollingProbe fetchSpy={fetchSpy} value="second" />);

    await waitFor(() => {
      expect(screen.getByText("second")).toBeInTheDocument();
    });
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    expect(fetchSpy).toHaveBeenLastCalledWith("second");
  });

  it("aborts the in-flight request on unmount", () => {
    const onAbort = vi.fn();
    const { unmount } = render(<AbortProbe onAbort={onAbort} />);

    unmount();

    expect(onAbort).toHaveBeenCalledTimes(1);
  });
});
