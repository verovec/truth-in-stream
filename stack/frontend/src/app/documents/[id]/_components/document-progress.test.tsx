import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { DocumentRecord } from "@/lib/documents/api";
import { DocumentProgress } from "./document-progress";

function docRecord(over: Partial<DocumentRecord> = {}): DocumentRecord {
  return {
    id: "d1",
    title: "Doc",
    status: "ready",
    analysisStatus: "analysing",
    analysisError: "",
    contentType: "application/pdf",
    sizeBytes: 10,
    pageCount: 1,
    sentencesTotal: 12,
    sentencesProcessed: 3,
    analysisRuns: 0,
    analyzedAt: "",
    createdAt: "2026-07-09T09:00:00Z",
    updatedAt: "2026-07-09T09:00:00Z",
    ...over,
  };
}

describe("DocumentProgress", () => {
  test("shows the bar and the processed/total counter while analysing", () => {
    render(<DocumentProgress document={docRecord()} />);
    expect(screen.getByText(fr.app.viewer.progress.analysing)).toBeInTheDocument();
    expect(screen.getByText("3 / 12 phrases")).toBeInTheDocument();
    const bar = screen.getByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "25");
  });

  test("renders nothing outside the analysing state", () => {
    const { container } = render(
      <DocumentProgress document={docRecord({ analysisStatus: "complete" })} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  test("a zero-total document reads as 0% rather than dividing by zero", () => {
    render(
      <DocumentProgress
        document={docRecord({ sentencesTotal: 0, sentencesProcessed: 0 })}
      />,
    );
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "0");
  });
});
