import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { ApiError } from "@/lib/http";
import type { DocumentRecord } from "@/lib/documents/api";
import { ReanalyseControl } from "./reanalyse-control";

afterEach(() => vi.restoreAllMocks());

function docRecord(over: Partial<DocumentRecord> = {}): DocumentRecord {
  return {
    id: "d1",
    title: "Doc",
    status: "ready",
    analysisStatus: "complete",
    analysisError: "",
    contentType: "application/pdf",
    sizeBytes: 10,
    pageCount: 1,
    sentencesTotal: 3,
    sentencesProcessed: 3,
    analysisRuns: 1,
    analyzedAt: "2026-07-09T10:00:00Z",
    createdAt: "2026-07-09T09:00:00Z",
    updatedAt: "2026-07-09T10:00:00Z",
    ...over,
  };
}

describe("ReanalyseControl", () => {
  test("confirms before firing, then calls reanalyse and onStarted", async () => {
    const reanalyse = vi.fn().mockResolvedValue(undefined);
    const onStarted = vi.fn();
    render(
      <ReanalyseControl
        document={docRecord()}
        reanalyse={reanalyse}
        onStarted={onStarted}
      />,
    );

    fireEvent.click(
      screen.getByRole("button", { name: fr.app.viewer.reanalyse.action }),
    );
    expect(reanalyse).not.toHaveBeenCalled();

    fireEvent.click(
      screen.getByRole("button", { name: fr.app.viewer.reanalyse.confirmYes }),
    );
    await waitFor(() => expect(reanalyse).toHaveBeenCalledWith("d1"));
    expect(onStarted).toHaveBeenCalledTimes(1);
  });

  test("surfaces a 409 concurrent run gracefully without calling onStarted", async () => {
    const reanalyse = vi
      .fn()
      .mockRejectedValue(new ApiError("already", 409));
    const onStarted = vi.fn();
    render(
      <ReanalyseControl
        document={docRecord()}
        reanalyse={reanalyse}
        onStarted={onStarted}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: fr.app.viewer.reanalyse.action }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: fr.app.viewer.reanalyse.confirmYes }),
    );
    expect(
      await screen.findByText(fr.app.viewer.reanalyse.errors.conflict),
    ).toBeInTheDocument();
    expect(onStarted).not.toHaveBeenCalled();
  });

  test("is disabled while the document is already analysing", () => {
    render(
      <ReanalyseControl
        document={docRecord({ analysisStatus: "analysing" })}
        reanalyse={vi.fn()}
        onStarted={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("button", { name: fr.app.viewer.reanalyse.running }),
    ).toBeDisabled();
  });

  test("the retry variant carries the retry label", () => {
    render(
      <ReanalyseControl
        document={docRecord({ analysisStatus: "failed" })}
        reanalyse={vi.fn()}
        onStarted={vi.fn()}
        variant="retry"
      />,
    );
    expect(
      screen.getByRole("button", { name: fr.app.viewer.reanalyse.retry }),
    ).toBeInTheDocument();
  });
});
