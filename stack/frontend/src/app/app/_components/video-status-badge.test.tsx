import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { VideoKindBadge } from "./video-status-badge";

describe("VideoKindBadge", () => {
  test.each([
    ["sample", "Sample"],
    ["upload", "Upload"],
    ["youtube", "YouTube"],
  ] as const)("labels a %s video as %s", (kind, label) => {
    render(<VideoKindBadge kind={kind} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });
});
