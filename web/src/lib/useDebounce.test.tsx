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

import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useDebouncedValue } from "@/lib/useDebounce";

function DebounceProbe({ value }: { value: string }) {
  const debounced = useDebouncedValue(value, 300);
  return <output aria-label="debounced">{debounced}</output>;
}

describe("useDebouncedValue", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("publishes the latest value only after the debounce delay", () => {
    vi.useFakeTimers();
    const { rerender } = render(<DebounceProbe value="first" />);
    expect(screen.getByLabelText("debounced")).toHaveTextContent("first");

    rerender(<DebounceProbe value="second" />);
    expect(screen.getByLabelText("debounced")).toHaveTextContent("first");

    act(() => {
      vi.advanceTimersByTime(299);
    });
    expect(screen.getByLabelText("debounced")).toHaveTextContent("first");

    rerender(<DebounceProbe value="third" />);
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(screen.getByLabelText("debounced")).toHaveTextContent("third");
  });
});
