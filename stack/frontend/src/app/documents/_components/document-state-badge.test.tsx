import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { DocumentStateBadge } from "./document-state-badge";

describe("DocumentStateBadge", () => {
  test("shows the upload status while the document is not ready", () => {
    render(<DocumentStateBadge status="pending" analysisStatus="none" />);
    expect(screen.getByText(fr.app.documents.status.pending)).toBeInTheDocument();
  });

  test("shows the analysis status once the upload is ready", () => {
    render(<DocumentStateBadge status="ready" analysisStatus="analysing" />);
    expect(screen.getByText(fr.app.documents.analysis.analysing)).toBeInTheDocument();
  });

  test("a ready, analysed document reads as analysed", () => {
    render(<DocumentStateBadge status="ready" analysisStatus="complete" />);
    expect(screen.getByText(fr.app.documents.analysis.complete)).toBeInTheDocument();
  });
});
