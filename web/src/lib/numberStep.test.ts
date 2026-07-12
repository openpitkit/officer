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

import { describe, expect, it } from "vitest";

import {
  isDecimalRangeValid,
  isDecimalString,
  isNonNegativeDecimalString,
  isNonNegativeIntegerRangeValid,
  isOptionalPositiveDecimalString,
  isPositiveDecimalString,
  smartStep,
  stepValue,
  subtractDecimalStrings,
} from "@/lib/numberStep";

describe("isDecimalRangeValid", () => {
  it("validates visible single and between range fields", () => {
    expect(isDecimalRangeValid("eq", "12.5", "hidden-invalid")).toBe(true);
    expect(isDecimalRangeValid("eq", "12x", "")).toBe(false);
    expect(isDecimalRangeValid("between", "10", "20")).toBe(true);
    expect(isDecimalRangeValid("between", "", "")).toBe(true);
    expect(isDecimalRangeValid("between", "10", "")).toBe(false);
    expect(isDecimalRangeValid("between", "", "20")).toBe(false);
    expect(isDecimalRangeValid("between", "20", "10")).toBe(false);
    expect(isDecimalRangeValid("between", "10", "20x")).toBe(false);
  });
});

describe("isDecimalString", () => {
  it("accepts empty and decimal text values", () => {
    expect(isDecimalString("")).toBe(true);
    expect(isDecimalString("12")).toBe(true);
    expect(isDecimalString("-12.5")).toBe(true);
    expect(isDecimalString(".5")).toBe(true);
    expect(isDecimalString("12.")).toBe(true);
  });

  it("rejects non-decimal text values", () => {
    expect(isDecimalString("word")).toBe(false);
    expect(isDecimalString("12a")).toBe(false);
  });
});

describe("decimal mutation validators", () => {
  it("distinguishes positive and non-negative required decimals", () => {
    expect(isPositiveDecimalString("0.01")).toBe(true);
    expect(isPositiveDecimalString("0")).toBe(false);
    expect(isPositiveDecimalString("-0.01")).toBe(false);
    expect(isPositiveDecimalString("")).toBe(false);

    expect(isNonNegativeDecimalString("0")).toBe(true);
    expect(isNonNegativeDecimalString("1.25")).toBe(true);
    expect(isNonNegativeDecimalString("-1")).toBe(false);
    expect(isNonNegativeDecimalString("")).toBe(false);
  });

  it("accepts an empty optional price but requires a positive provided price", () => {
    expect(isOptionalPositiveDecimalString("")).toBe(true);
    expect(isOptionalPositiveDecimalString("  ")).toBe(true);
    expect(isOptionalPositiveDecimalString("10.5")).toBe(true);
    expect(isOptionalPositiveDecimalString("0")).toBe(false);
    expect(isOptionalPositiveDecimalString("-10.5")).toBe(false);
    expect(isOptionalPositiveDecimalString("price")).toBe(false);
  });
});

describe("isNonNegativeIntegerRangeValid", () => {
  it("rejects signed and fractional counts and validates visible bounds", () => {
    expect(isNonNegativeIntegerRangeValid("eq", "12", "hidden-invalid")).toBe(true);
    expect(isNonNegativeIntegerRangeValid("eq", "-1", "")).toBe(false);
    expect(isNonNegativeIntegerRangeValid("eq", "1.5", "")).toBe(false);
    expect(isNonNegativeIntegerRangeValid("between", "1", "2")).toBe(true);
    expect(isNonNegativeIntegerRangeValid("between", "", "")).toBe(true);
    expect(isNonNegativeIntegerRangeValid("between", "1", "")).toBe(false);
    expect(isNonNegativeIntegerRangeValid("between", "", "2")).toBe(false);
    expect(isNonNegativeIntegerRangeValid("between", "2", "1")).toBe(false);
  });
});

describe("smartStep", () => {
  it("scales the step with the value magnitude", () => {
    expect(smartStep("0")).toBe("0.1");
    expect(smartStep("0.5")).toBe("0.1");
    expect(smartStep("1")).toBe("1");
    expect(smartStep("9.9")).toBe("1");
    expect(smartStep("10")).toBe("5");
    expect(smartStep("99")).toBe("5");
    expect(smartStep("100")).toBe("10");
    expect(smartStep("5000")).toBe("50");
    expect(smartStep("65000")).toBe("100");
    expect(smartStep("500000")).toBe("500");
    expect(smartStep("5000000")).toBe("1000");
  });

  it("uses magnitude, so negatives mirror positives", () => {
    expect(smartStep("-0.5")).toBe("0.1");
    expect(smartStep("-50")).toBe("5");
  });
});

describe("stepValue", () => {
  it("nudges by the magnitude-scaled step", () => {
    expect(stepValue("0.5", 1)).toBe("0.6");
    expect(stepValue("5", 1)).toBe("6");
    expect(stepValue("50", 1)).toBe("55");
    expect(stepValue("100", 1)).toBe("110");
    expect(stepValue("65000", 1)).toBe("65100");
  });

  it("steps down symmetrically", () => {
    expect(stepValue("0.6", -1)).toBe("0.5");
    expect(stepValue("55", -1)).toBe("50");
  });

  it("avoids binary-float drift", () => {
    expect(stepValue("0.2", 1)).toBe("0.3");
    expect(stepValue("0.3", -1)).toBe("0.2");
  });

  it("treats empty input as zero", () => {
    expect(stepValue("", 1)).toBe("0.1");
  });

  it("clamps to the minimum (0 by default)", () => {
    expect(stepValue("0", -1)).toBe("0");
    expect(stepValue("0.05", -1)).toBe("0");
  });

  it("honors a custom minimum", () => {
    expect(stepValue("1", -1, { min: "1" })).toBe("1");
  });

  it("allows signed values when the minimum is disabled", () => {
    expect(stepValue("0", -1, { min: null })).toBe("-0.1");
    expect(stepValue("-50", -1, { min: null })).toBe("-55");
  });

  it("returns unparsable input unchanged", () => {
    expect(stepValue("abc", 1)).toBe("abc");
  });

  it("preserves very small decimal values", () => {
    expect(stepValue("0.0000001", 1)).toBe("0.1000001");
  });

  it("steps values beyond MAX_SAFE_INTEGER exactly", () => {
    expect(stepValue("9999999999999999", 1)).toBe("10000000000000999");
  });
});

describe("subtractDecimalStrings", () => {
  it("subtracts decimal strings without binary-float drift", () => {
    expect(subtractDecimalStrings("2", "2")).toBe("0");
    expect(subtractDecimalStrings("2.50", "1.25")).toBe("1.25");
    expect(subtractDecimalStrings("0.3", "0.2")).toBe("0.1");
  });

  it("clamps negative subtraction results to zero", () => {
    expect(subtractDecimalStrings("1", "2")).toBe("0");
    expect(subtractDecimalStrings("0.1", "0.25")).toBe("0");
  });

  it("returns null for invalid decimal strings", () => {
    expect(subtractDecimalStrings("2", "word")).toBeNull();
  });
});
