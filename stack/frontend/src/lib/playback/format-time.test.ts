import { describe, expect, test } from "vitest";
import { formatTime } from "./format-time";

describe("formatTime", () => {
  test.each([
    [0, "0:00"],
    [7.9, "0:07"],
    [59, "0:59"],
    [60, "1:00"],
    [754, "12:34"],
    [3599, "59:59"],
    [3600, "1:00:00"],
    [7325, "2:02:05"],
  ])("formats %s seconds as %s", (seconds, expected) => {
    expect(formatTime(seconds)).toBe(expected);
  });

  test("clamps negative and non-finite input to zero", () => {
    expect(formatTime(-3)).toBe("0:00");
    expect(formatTime(Number.NaN)).toBe("0:00");
    expect(formatTime(Number.POSITIVE_INFINITY)).toBe("0:00");
  });
});
