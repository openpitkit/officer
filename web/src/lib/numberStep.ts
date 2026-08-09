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

// Smart stepping for decimal text fields: the nudge size scales with the
// value's magnitude, so prices/amounts feel fine-grained when small and coarse
// when large - fractions, then units, then fives, then larger round increments.

interface DecimalValue {
  scale: number;
  units: bigint;
}

const DECIMAL_PATTERN = /^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))$/;

export function isDecimalString(value: string): boolean {
  const trimmed = value.trim();
  return trimmed === "" || DECIMAL_PATTERN.test(trimmed);
}

/** Whether a required decimal field is syntactically valid and non-negative. */
export function isNonNegativeDecimalString(value: string): boolean {
  const decimal = parseDecimal(value);
  return decimal !== null && decimal.units >= 0n;
}

/** Whether a required decimal field is syntactically valid and strictly positive. */
export function isPositiveDecimalString(value: string): boolean {
  const decimal = parseDecimal(value);
  return decimal !== null && decimal.units > 0n;
}

/** Whether a value is accepted as an open base quantity - leaves. The API
 *  records it verbatim, refusing surrounding space and exponent notation, so
 *  the untrimmed string is what gets checked. */
export function isOpenQuantityString(value: string): boolean {
  return value === value.trim() && isNonNegativeDecimalString(value);
}

/** Whether an optional decimal field is empty or strictly positive. */
export function isOptionalPositiveDecimalString(value: string): boolean {
  return value.trim() === "" || isPositiveDecimalString(value);
}

/** Whether an optional integer field is empty or a non-negative integer. */
export function isOptionalNonNegativeIntegerString(value: string): boolean {
  const trimmed = value.trim();
  return trimmed === "" || /^\d+$/.test(trimmed);
}

function parseDecimal(value: string): DecimalValue | null {
  const match = DECIMAL_PATTERN.exec(value.trim());
  if (match === null) {
    return null;
  }

  const [, sign, intPart = "", fracPart = "", leadingFracPart = ""] = match;
  const fraction = fracPart === "" ? leadingFracPart : fracPart;
  const digits = `${intPart || "0"}${fraction}`.replace(/^0+(?=\d)/, "");
  const magnitude = BigInt(digits === "" ? "0" : digits);
  return {
    scale: fraction.length,
    units: sign === "-" && magnitude !== 0n ? -magnitude : magnitude,
  };
}

function decimalLiteral(value: string): DecimalValue {
  const decimal = parseDecimal(value);
  if (decimal === null) {
    throw new Error(`invalid decimal literal: ${value}`);
  }
  return decimal;
}

function scaleMultiplier(scale: number): bigint {
  return 10n ** BigInt(scale);
}

function unitsAtScale(value: DecimalValue, scale: number): bigint {
  return value.units * scaleMultiplier(scale - value.scale);
}

function compareDecimals(a: DecimalValue, b: DecimalValue): number {
  const scale = Math.max(a.scale, b.scale);
  const left = unitsAtScale(a, scale);
  const right = unitsAtScale(b, scale);
  if (left < right) return -1;
  if (left > right) return 1;
  return 0;
}

export function compareDecimalStrings(a: string, b: string): number | null {
  const left = parseDecimal(a);
  const right = parseDecimal(b);
  if (left === null || right === null) {
    return null;
  }
  return compareDecimals(left, right);
}

/** Whether every visible field in a decimal range control is valid. */
export function isDecimalRangeValid(
  operator: string,
  min: string,
  max: string,
): boolean {
  if (!isDecimalString(min)) {
    return false;
  }
  if (operator !== "between") {
    return true;
  }
  if (!isDecimalString(max)) {
    return false;
  }
  const minEmpty = min.trim() === "";
  const maxEmpty = max.trim() === "";
  if (minEmpty || maxEmpty) {
    return minEmpty && maxEmpty;
  }
  return compareDecimalStrings(min, max) !== 1;
}

/** Whether every visible field in a non-negative integer range is valid. */
export function isNonNegativeIntegerRangeValid(
  operator: string,
  min: string,
  max: string,
): boolean {
  if (!isOptionalNonNegativeIntegerString(min)) {
    return false;
  }
  if (operator !== "between") {
    return true;
  }
  if (!isOptionalNonNegativeIntegerString(max)) {
    return false;
  }
  const minEmpty = min.trim() === "";
  const maxEmpty = max.trim() === "";
  if (minEmpty || maxEmpty) {
    return minEmpty && maxEmpty;
  }
  return BigInt(min.trim()) <= BigInt(max.trim());
}

function absDecimal(value: DecimalValue): DecimalValue {
  return {
    scale: value.scale,
    units: value.units < 0n ? -value.units : value.units,
  };
}

function addDecimals(a: DecimalValue, b: DecimalValue): DecimalValue {
  const scale = Math.max(a.scale, b.scale);
  return {
    scale,
    units: unitsAtScale(a, scale) + unitsAtScale(b, scale),
  };
}

function negateDecimal(value: DecimalValue): DecimalValue {
  return {
    scale: value.scale,
    units: -value.units,
  };
}

function formatDecimal(value: DecimalValue): string {
  const negative = value.units < 0n;
  const magnitude = negative ? -value.units : value.units;
  if (value.scale === 0) {
    return `${negative ? "-" : ""}${magnitude.toString()}`;
  }

  const padded = magnitude.toString().padStart(value.scale + 1, "0");
  const intPart = padded.slice(0, -value.scale);
  const fracPart = padded.slice(-value.scale).replace(/0+$/, "");
  const formatted = fracPart === "" ? intPart : `${intPart}.${fracPart}`;
  return `${negative ? "-" : ""}${formatted}`;
}

export function subtractDecimalStrings(a: string, b: string): string | null {
  const left = parseDecimal(a);
  const right = parseDecimal(b);
  if (left === null || right === null) {
    return null;
  }
  const result = addDecimals(left, negateDecimal(right));
  if (compareDecimals(result, decimalLiteral("0")) < 0) {
    return "0";
  }
  return formatDecimal(result);
}

const STEP_BANDS = [
  { threshold: decimalLiteral("1"), step: "0.1" },
  { threshold: decimalLiteral("10"), step: "1" },
  { threshold: decimalLiteral("100"), step: "5" },
  { threshold: decimalLiteral("1000"), step: "10" },
  { threshold: decimalLiteral("10000"), step: "50" },
  { threshold: decimalLiteral("100000"), step: "100" },
  { threshold: decimalLiteral("1000000"), step: "500" },
] as const;

/**
 * Pick the nudge step for a decimal string, scaled to its magnitude. The step
 * is recomputed from the current value on every nudge, so crossing a band
 * changes the granularity automatically.
 */
export function smartStep(value: string): string {
  const current = parseDecimal(value.trim() === "" ? "0" : value);
  if (current === null) {
    return "0.1";
  }

  const magnitude = absDecimal(current);
  for (const band of STEP_BANDS) {
    if (compareDecimals(magnitude, band.threshold) < 0) {
      return band.step;
    }
  }
  return "1000";
}

export interface StepOptions {
  /** Lower clamp; defaults to 0. Use null for signed values. */
  min?: string | null;
}

/**
 * Nudge a decimal string by one smart step in `direction`. Empty input is
 * treated as 0; the result is clamped to `min` (default 0, null disables the
 * clamp) and returned as a trimmed decimal string. Unparsable input is returned
 * unchanged.
 */
export function stepValue(
  value: string,
  direction: 1 | -1,
  options: StepOptions = {},
): string {
  const trimmed = value.trim();
  const current = parseDecimal(trimmed === "" ? "0" : trimmed);
  const min = options.min === null ? null : parseDecimal(options.min ?? "0");
  if (current === null || (options.min !== null && min === null)) {
    return value;
  }

  const step = decimalLiteral(smartStep(trimmed));
  const next = addDecimals(
    current,
    direction === 1 ? step : negateDecimal(step),
  );
  return min !== null && compareDecimals(next, min) < 0
    ? formatDecimal(min)
    : formatDecimal(next);
}
