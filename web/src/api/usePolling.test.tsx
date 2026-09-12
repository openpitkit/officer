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

import { useCallback, useEffect } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { usePolling } from "@/api/usePolling";

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
} {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function PollingProbe({
  fetchSpy,
  value,
}: {
  fetchSpy: (value: string) => void;
  value: string;
}) {
  const fetcher = useCallback(async () => {
    fetchSpy(value);
    return value;
  }, [fetchSpy, value]);
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

function ReloadProbe({
  fetcher,
}: {
  fetcher: (signal: AbortSignal) => Promise<string>;
}) {
  const { load, reload } = usePolling(fetcher, 60000);
  return (
    <>
      <span>{load.state === "ready" ? load.data : load.state}</span>
      <span>{load.error}</span>
      <button type="button" onClick={reload}>
        reload
      </button>
    </>
  );
}

function IdentityProbe({
  fetcher,
  onLoadChange,
}: {
  fetcher: (signal: AbortSignal) => Promise<string>;
  onLoadChange: () => void;
}) {
  const { load } = usePolling(fetcher, 60000);
  useEffect(onLoadChange, [load, onLoadChange]);
  return <span>{load.state === "ready" ? load.data : load.state}</span>;
}

describe("usePolling", () => {
  it("marks retained data stale and clears the error after recovery", async () => {
    vi.useFakeTimers();
    try {
      const fetcher = vi
        .fn<(signal: AbortSignal) => Promise<string>>()
        .mockResolvedValueOnce("first")
        .mockRejectedValueOnce(new Error("poll failed"))
        .mockResolvedValueOnce("recovered");

      await act(async () => {
        render(<ReloadProbe fetcher={fetcher} />);
      });
      expect(screen.getByText("first")).toBeInTheDocument();

      await act(() => vi.advanceTimersByTimeAsync(60000));
      expect(screen.getByText("first")).toBeInTheDocument();
      expect(screen.getByText("poll failed")).toBeInTheDocument();

      await act(() => vi.advanceTimersByTimeAsync(60000));
      expect(screen.getByText("recovered")).toBeInTheDocument();
      expect(screen.queryByText("poll failed")).not.toBeInTheDocument();
      expect(screen.queryByText("first")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not notify load consumers for an unchanged passive failure", async () => {
    vi.useFakeTimers();
    try {
      const onLoadChange = vi.fn();
      const fetcher = vi
        .fn<(signal: AbortSignal) => Promise<string>>()
        .mockResolvedValueOnce("first")
        .mockRejectedValueOnce(new Error("poll failed"))
        .mockRejectedValueOnce(new Error("poll failed"));

      await act(async () => {
        render(<IdentityProbe fetcher={fetcher} onLoadChange={onLoadChange} />);
      });
      await act(() => vi.advanceTimersByTimeAsync(60000));
      const changesAfterFirstFailure = onLoadChange.mock.calls.length;

      await act(() => vi.advanceTimersByTimeAsync(60000));

      expect(onLoadChange).toHaveBeenCalledTimes(changesAfterFirstFailure);
    } finally {
      vi.useRealTimers();
    }
  });

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

  it("keeps ready data visible while a manual reload is in flight", async () => {
    const user = userEvent.setup();
    const first = deferred<string>();
    const second = deferred<string>();
    const fetcher = vi
      .fn<(signal: AbortSignal) => Promise<string>>()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    render(<ReloadProbe fetcher={fetcher} />);
    first.resolve("first");

    await waitFor(() => {
      expect(screen.getByText("first")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "reload" }));

    await waitFor(() => {
      expect(fetcher).toHaveBeenCalledTimes(2);
    });
    expect(screen.getByText("first")).toBeInTheDocument();

    second.resolve("second");

    await waitFor(() => {
      expect(screen.getByText("second")).toBeInTheDocument();
    });
  });

  it("surfaces the error when a manual reload fails while ready", async () => {
    const user = userEvent.setup();
    const first = deferred<string>();
    const second = deferred<string>();
    const fetcher = vi
      .fn<(signal: AbortSignal) => Promise<string>>()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    render(<ReloadProbe fetcher={fetcher} />);
    first.resolve("first");

    await waitFor(() => {
      expect(screen.getByText("first")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "reload" }));

    await waitFor(() => {
      expect(fetcher).toHaveBeenCalledTimes(2);
    });
    // Stale data stays visible while the reload is in flight.
    expect(screen.getByText("first")).toBeInTheDocument();

    second.reject(new Error("reload failed"));

    // A failed reload replaces the stale data with the error state instead of
    // silently retaining it.
    await waitFor(() => {
      expect(screen.getByText("error")).toBeInTheDocument();
    });
    expect(screen.queryByText("first")).not.toBeInTheDocument();
  });
});
