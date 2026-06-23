import { describe, expect, test } from "vitest";

import { filenameFromDisposition } from "./export";

describe("filenameFromDisposition", () => {
  test("extracts a quoted filename", () => {
    expect(
      filenameFromDisposition('attachment; filename="my-video.srt"'),
    ).toBe("my-video.srt");
  });

  test("extracts an unquoted filename", () => {
    expect(filenameFromDisposition("attachment; filename=claims.csv")).toBe(
      "claims.csv",
    );
  });

  test("returns null when the header is absent", () => {
    expect(filenameFromDisposition(null)).toBeNull();
  });

  test("returns null when no filename is present", () => {
    expect(filenameFromDisposition("attachment")).toBeNull();
  });
});
