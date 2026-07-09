import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import type { LibraryDocument } from "@/lib/documents/api";
import { DocumentCard } from "./document-card";

function doc(over: Partial<LibraryDocument> = {}): LibraryDocument {
  return {
    id: "doc-1",
    title: "Rapport",
    status: "ready",
    analysisStatus: "complete",
    analysisError: "",
    contentType: "application/pdf",
    sizeBytes: 2048,
    pageCount: 1,
    sentencesTotal: 4,
    sentencesProcessed: 4,
    analysisRuns: 1,
    analyzedAt: "2026-07-09T10:00:00Z",
    createdAt: "2026-07-09T09:00:00Z",
    updatedAt: "2026-07-09T10:00:00Z",
    credibleClaims: 3,
    disputedClaims: 1,
    ...over,
  };
}

describe("DocumentCard", () => {
  test("renders the title, a pluralized page count, and verdict counts", () => {
    render(<DocumentCard doc={doc({ pageCount: 3 })} />);
    expect(screen.getByRole("heading", { name: "Rapport" })).toBeInTheDocument();
    expect(screen.getByText("3 pages")).toBeInTheDocument();
    expect(screen.getByText("3 fiables")).toBeInTheDocument();
    expect(screen.getByText("1 contesté")).toBeInTheDocument();
  });

  test("the whole tile links to the document viewer", () => {
    render(<DocumentCard doc={doc({ id: "doc-42", title: "Rapport" })} />);
    expect(screen.getByRole("link", { name: "Rapport" })).toHaveAttribute(
      "href",
      "/documents/doc-42",
    );
  });

  test("pluralizes the page count with the locale rule (1 page singular)", () => {
    render(<DocumentCard doc={doc({ pageCount: 1 })} />);
    expect(screen.getByText("1 page")).toBeInTheDocument();
  });

  test("omits verdict counts until the document is analysed", () => {
    render(<DocumentCard doc={doc({ analysisStatus: "analysing" })} />);
    expect(screen.queryByText(/fiable/)).not.toBeInTheDocument();
    expect(screen.queryByText(/contesté/)).not.toBeInTheDocument();
  });

  test("shows no verdict pills for an analysed document with no findings", () => {
    render(<DocumentCard doc={doc({ credibleClaims: 0, disputedClaims: 0 })} />);
    expect(screen.queryByText(/fiable/)).not.toBeInTheDocument();
    expect(screen.queryByText(/contesté/)).not.toBeInTheDocument();
  });
});
