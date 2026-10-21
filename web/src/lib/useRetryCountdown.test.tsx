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

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useRetryCountdown } from "@/lib/useRetryCountdown";

function CountdownProbe({ start }: { start: number }) {
  const [seconds, startCountdown] = useRetryCountdown();
  return (
    <>
      <output aria-label="seconds left">{seconds}</output>
      <button type="button" onClick={() => startCountdown(start)}>
        start
      </button>
    </>
  );
}

// The whole count, not a part of it: "29" must not pass for "9".
function secondsLeft() {
  return Number(screen.getByLabelText("seconds left").textContent);
}

// Moves the clock one second per step, letting each tick render.
function tick(seconds: number) {
  for (let i = 0; i < seconds; i++) {
    act(() => {
      vi.advanceTimersByTime(1000);
    });
  }
}

describe("useRetryCountdown", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("stays at 0 until a countdown starts", () => {
    render(<CountdownProbe start={3} />);

    tick(2);

    expect(secondsLeft()).toBe(0);
  });

  it("counts down once a second and stops at 0", () => {
    render(<CountdownProbe start={3} />);
    fireEvent.click(screen.getByRole("button", { name: "start" }));
    expect(secondsLeft()).toBe(3);

    act(() => {
      vi.advanceTimersByTime(999);
    });
    expect(secondsLeft()).toBe(3);
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(secondsLeft()).toBe(2);

    tick(2);
    expect(secondsLeft()).toBe(0);
    tick(2);
    expect(secondsLeft()).toBe(0);
  });

  it("shows the true remainder after the clock jumps without ticks", () => {
    render(<CountdownProbe start={30} />);
    fireEvent.click(screen.getByRole("button", { name: "start" }));

    // A frozen tab or a sleeping machine: 20 s pass and no timer fires.
    vi.setSystemTime(Date.now() + 20_000);
    tick(1);

    expect(secondsLeft()).toBe(9);
  });

  it("reaches 0 although the clock moved back to show the same count", () => {
    render(<CountdownProbe start={3} />);
    fireEvent.click(screen.getByRole("button", { name: "start" }));

    // The clock moves back a second, so the next tick shows 3 again.
    vi.setSystemTime(Date.now() - 1000);
    tick(1);
    expect(secondsLeft()).toBe(3);

    tick(3);
    expect(secondsLeft()).toBe(0);
  });

  it("starts again once it has run out", () => {
    render(<CountdownProbe start={2} />);
    fireEvent.click(screen.getByRole("button", { name: "start" }));
    tick(2);
    expect(secondsLeft()).toBe(0);

    fireEvent.click(screen.getByRole("button", { name: "start" }));
    expect(secondsLeft()).toBe(2);
    tick(1);
    expect(secondsLeft()).toBe(1);
  });

  it("starts again at the same deadline as the run that ran out", () => {
    render(<CountdownProbe start={2} />);
    const startedAt = Date.now();
    fireEvent.click(screen.getByRole("button", { name: "start" }));
    tick(2);
    expect(secondsLeft()).toBe(0);

    // The clock moves back to the first start, so the deadline repeats.
    vi.setSystemTime(startedAt);
    fireEvent.click(screen.getByRole("button", { name: "start" }));
    expect(secondsLeft()).toBe(2);
    tick(2);
    expect(secondsLeft()).toBe(0);
  });
});
