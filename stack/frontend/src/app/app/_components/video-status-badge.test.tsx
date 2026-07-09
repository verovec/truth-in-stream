import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { VideoKindBadge, VideoStatusBadge } from "./video-status-badge";

describe("VideoStatusBadge", () => {
  test.each([
    ["ready", fr.app.library.status.ready],
    ["pending", fr.app.library.status.pending],
    ["failed", fr.app.library.status.failed],
  ] as const)("labels a %s video as %s", (status, label) => {
    render(<VideoStatusBadge status={status} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });
});

describe("VideoKindBadge", () => {
  test.each([
    ["sample", fr.app.library.kind.sample],
    ["upload", fr.app.library.kind.upload],
    ["youtube", fr.app.library.kind.youtube],
  ] as const)("labels a %s video as %s", (kind, label) => {
    render(<VideoKindBadge kind={kind} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });
});
