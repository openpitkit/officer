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

import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useRowVerification, useSymbolSearch } from "@/components/venues";

function deferred<Result>() {
  let resolve!: (result: Result) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<Result>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

describe("useSymbolSearch", () => {
  it("ignores whitespace queries", async () => {
    const search = vi.fn().mockResolvedValue([]);
    const { result } = renderHook(() => useSymbolSearch<string>(search));
    act(() => result.current.onQueryChange("   "));
    await act(() => result.current.onSearch());
    expect(search).not.toHaveBeenCalled();
    expect(result.current).toMatchObject({ searching: false, searched: false });
  });

  it("trims searches and resets results and filter on every run", async () => {
    const first = deferred<string[] | null>();
    const second = deferred<string[] | null>();
    const search = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    const { result } = renderHook(() => useSymbolSearch<string>(search));
    act(() => result.current.onQueryChange("  AAPL  "));
    act(() => result.current.onFilterChange("old filter"));
    let run!: Promise<void>;
    act(() => {
      run = result.current.onSearch();
    });
    expect(search).toHaveBeenCalledExactlyOnceWith("AAPL");
    expect(result.current).toMatchObject({
      query: "  AAPL  ",
      results: [],
      filterQuery: "",
      searching: true,
      searched: true,
    });
    await act(async () => {
      first.resolve(["AAPL"]);
      await run;
    });
    expect(result.current).toMatchObject({
      results: ["AAPL"],
      searching: false,
      searched: true,
    });

    act(() => result.current.onFilterChange("apple"));
    act(() => {
      run = result.current.onSearch();
    });
    expect(result.current).toMatchObject({
      results: [],
      filterQuery: "",
      searching: true,
      searched: true,
    });
    await act(async () => {
      second.resolve(null);
      await run;
    });
    expect(result.current).toMatchObject({
      results: [],
      filterQuery: "",
      searching: false,
      searched: true,
    });
  });

  it("query changes clear results, filter and the searched flag", async () => {
    const search = vi.fn().mockResolvedValue(["AAPL"]);
    const { result } = renderHook(() => useSymbolSearch<string>(search));
    act(() => result.current.onQueryChange("AAPL"));
    await act(() => result.current.onSearch());
    act(() => result.current.onFilterChange("apple"));
    expect(result.current).toMatchObject({
      results: ["AAPL"],
      filterQuery: "apple",
      searched: true,
    });
    act(() => result.current.onQueryChange("MSFT"));
    expect(result.current).toMatchObject({
      query: "MSFT",
      results: [],
      filterQuery: "",
      searched: false,
    });
  });
});

describe("useRowVerification", () => {
  it.each(["exists", null])(
    "forgets the recorded outcome %s",
    async (outcome) => {
      const verify = vi.fn().mockResolvedValue(outcome);
      const { result } = renderHook(() => useRowVerification<string>(verify));
      await act(() => result.current.verifyRow("AAPL"));
      act(() => result.current.forget("AAPL"));
      expect(Object.hasOwn(result.current.outcomes, "AAPL")).toBe(false);
    },
  );

  it.each(["exists", null, "throw"])(
    "discards a late response %s after forget",
    async (outcome) => {
      const pending = deferred<string | null>();
      const { result } = renderHook(() =>
        useRowVerification<string>(() => pending.promise),
      );
      let run!: Promise<void>;
      act(() => {
        run = result.current.verifyRow("AAPL");
      });
      act(() => result.current.forget("AAPL"));
      await act(async () => {
        if (outcome === "throw") pending.reject(new Error("Venue offline"));
        else pending.resolve(outcome);
        await run;
      });
      expect(Object.hasOwn(result.current.outcomes, "AAPL")).toBe(false);
    },
  );

  it("records the message from a throwing verify", async () => {
    const verify = vi.fn().mockRejectedValue(new Error("Venue offline"));
    const { result } = renderHook(() => useRowVerification<string>(verify));
    await act(() => result.current.verifyRow("AAPL"));
    expect(result.current.outcomes).toEqual({
      AAPL: { failed: true, message: "Venue offline" },
    });
  });

  it("keeps independently pending symbols disabled and records null as failed", async () => {
    const first = deferred<string | null>();
    const second = deferred<string | null>();
    const verify = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    const { result } = renderHook(() => useRowVerification<string>(verify));
    let runA!: Promise<void>;
    let runB!: Promise<void>;
    act(() => {
      runA = result.current.verifyRow("AAPL");
    });
    act(() => {
      runB = result.current.verifyRow("MSFT");
    });
    expect(verify.mock.calls).toEqual([["AAPL"], ["MSFT"]]);
    expect([
      result.current.isVerifying("AAPL"),
      result.current.isVerifying("MSFT"),
      result.current.isVerifying("OTHER"),
    ]).toEqual([true, true, false]);
    await act(async () => {
      second.resolve(null);
      await runB;
    });
    expect([
      result.current.isVerifying("AAPL"),
      result.current.isVerifying("MSFT"),
    ]).toEqual([true, false]);
    expect(result.current.outcomes).toEqual({ MSFT: { failed: true } });
    await act(async () => {
      first.resolve("exists");
      await runA;
    });
    expect(result.current.isVerifying("AAPL")).toBe(false);
    expect(result.current.outcomes).toEqual({
      AAPL: "exists",
      MSFT: { failed: true },
    });
  });

  it.each(["latest", null])(
    "keeps the latest response %s when an older response arrives late",
    async (latest) => {
      const older = deferred<string | null>();
      const newer = deferred<string | null>();
      const verify = vi
        .fn()
        .mockReturnValueOnce(older.promise)
        .mockReturnValueOnce(newer.promise);
      const { result } = renderHook(() => useRowVerification<string>(verify));
      let oldRun!: Promise<void>;
      let newRun!: Promise<void>;
      act(() => {
        oldRun = result.current.verifyRow("AAPL");
      });
      act(() => {
        newRun = result.current.verifyRow("AAPL");
      });
      await act(async () => {
        newer.resolve(latest);
        await newRun;
      });
      expect(result.current.outcomes).toEqual({
        AAPL: latest === null ? { failed: true } : latest,
      });
      expect(result.current.isVerifying("AAPL")).toBe(true);
      await act(async () => {
        older.resolve("stale");
        await oldRun;
      });
      expect(result.current.outcomes).toEqual({
        AAPL: latest === null ? { failed: true } : latest,
      });
      expect(result.current.isVerifying("AAPL")).toBe(false);
    },
  );
});
