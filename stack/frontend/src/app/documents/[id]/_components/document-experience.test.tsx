import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type {
  DocumentAnalysis,
  DocumentDetail,
  DocumentRecord,
  DocumentSentence,
} from "@/lib/documents/api";
import type { LiveClaim } from "@/lib/live/claims";
import { DocumentExperience } from "./document-experience";
import type { PageHighlightSentence } from "./highlight-sentences";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function docRecord(over: Partial<DocumentRecord> = {}): DocumentRecord {
  return {
    id: "d1",
    title: "Rapport annuel",
    status: "ready",
    analysisStatus: "complete",
    analysisError: "",
    contentType: "application/pdf",
    sizeBytes: 10,
    pageCount: 2,
    sentencesTotal: 2,
    sentencesProcessed: 2,
    analysisRuns: 1,
    analyzedAt: "2026-07-09T10:00:00Z",
    createdAt: "2026-07-09T09:00:00Z",
    updatedAt: "2026-07-09T10:00:00Z",
    ...over,
  };
}

function credibleClaim(): LiveClaim {
  return {
    claimId: "c-1",
    text: "Baisse du chomage constatee.",
    status: "verified",
    source: "verified",
    verdict: "credible",
    basis: "evidence",
    confidence: 0.82,
    rationale: "Corrobore par la source.",
    matches: [
      {
        kind: "claim",
        claim: "Baisse du chomage",
        verdict: "corroborates",
        sources: [{ title: "INSEE", url: "https://insee.fr" }],
        similarity: 0.9,
      },
    ],
  };
}

function credibleSentence(): DocumentSentence {
  return {
    seq: 0,
    page: 1,
    text: "Le chomage a baisse.",
    occurrence: 1,
    skipReason: "",
    claims: [credibleClaim()],
  };
}

function skippedSentence(): DocumentSentence {
  return {
    seq: 1,
    page: 1,
    text: "Bonjour a tous.",
    occurrence: 1,
    skipReason: "not_a_claim",
    claims: [],
  };
}

function detail(over: Partial<DocumentDetail> = {}): DocumentDetail {
  return { document: docRecord(), pdfUrl: "https://get/d1.pdf", ...over };
}

function analysis(over: Partial<DocumentAnalysis> = {}): DocumentAnalysis {
  return {
    document: docRecord(),
    sentences: [credibleSentence(), skippedSentence()],
    ...over,
  };
}

// StubViewer stands in for the browser-only react-pdf viewer so the experience
// tests without pdf.js; it echoes the presigned URL it was handed.
function StubViewer({ url }: { url: string }) {
  return <div data-testid="pdf-viewer">{url}</div>;
}

// InteractiveStubViewer surfaces the highlight sentences the experience derives
// and the selection seam it wires, so the bidirectional sync can be tested
// without pdf.js: each highlight is a button that selects its sentence on click.
function InteractiveStubViewer({
  sentences = [],
  onSelect,
}: {
  sentences?: readonly PageHighlightSentence[];
  selectedSeq?: number | null;
  onSelect?: (seq: number) => void;
}) {
  return (
    <div data-testid="pdf-viewer">
      {sentences.map((sentence) => (
        <button
          key={sentence.seq}
          type="button"
          onClick={() => onSelect?.(sentence.seq)}
        >
          highlight-{sentence.seq}-{sentence.verdict}
        </button>
      ))}
    </div>
  );
}

describe("DocumentExperience", () => {
  test("a guest sees the whole PDF and every sentence with verdicts and working sources", async () => {
    render(
      <DocumentExperience
        documentId="d1"
        role="guest"
        loadDetail={async () => detail()}
        loadClaims={async () => analysis()}
        pdfViewer={StubViewer}
      />,
    );

    expect(await screen.findByTestId("pdf-viewer")).toHaveTextContent(
      "https://get/d1.pdf",
    );
    // Every analysed sentence is listed in document order.
    expect(screen.getByText("Le chomage a baisse.")).toBeInTheDocument();
    expect(screen.getByText("Bonjour a tous.")).toBeInTheDocument();
    // The credible verdict, confidence, and a working source link render.
    expect(screen.getByText(fr.app.claims.verdicts.credible)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "INSEE" })).toHaveAttribute(
      "href",
      "https://insee.fr",
    );
    // The skipped sentence shows its reason.
    expect(
      screen.getByText(fr.app.viewer.panel.skipReasons.not_a_claim),
    ).toBeInTheDocument();
    // A guest never sees the reanalyse control.
    expect(
      screen.queryByRole("button", { name: fr.app.viewer.reanalyse.action }),
    ).not.toBeInTheDocument();
  });

  test("selecting a sentence marks its row current (the shared selection seam)", async () => {
    render(
      <DocumentExperience
        documentId="d1"
        role="guest"
        loadDetail={async () => detail()}
        loadClaims={async () => analysis()}
        pdfViewer={StubViewer}
      />,
    );
    const header = await screen.findByText("Le chomage a baisse.");
    const row = header.closest("li");
    expect(row).not.toBeNull();
    expect(row).not.toHaveAttribute("aria-current");
    fireEvent.click(header);
    expect(row).toHaveAttribute("aria-current", "true");
  });

  test("only credible/disputed sentences reach the PDF, and a highlight click selects the panel row", async () => {
    render(
      <DocumentExperience
        documentId="d1"
        role="guest"
        loadDetail={async () => detail()}
        loadClaims={async () => analysis()}
        pdfViewer={InteractiveStubViewer}
      />,
    );
    // The credible sentence (seq 0) is highlighted; the skipped one (seq 1) is
    // panel-only, so it is never handed to the viewer.
    const highlight = await screen.findByRole("button", {
      name: "highlight-0-credible",
    });
    expect(
      screen.queryByRole("button", { name: /highlight-1/ }),
    ).not.toBeInTheDocument();

    // Clicking the highlight selects the sentence: its panel row becomes current.
    const row = screen.getByText("Le chomage a baisse.").closest("li");
    expect(row).not.toHaveAttribute("aria-current");
    fireEvent.click(highlight);
    expect(row).toHaveAttribute("aria-current", "true");
  });

  test("an admin can reanalyse: confirm fires the run", async () => {
    const reanalyse = vi.fn().mockResolvedValue(undefined);
    render(
      <DocumentExperience
        documentId="d1"
        role="admin"
        loadDetail={async () => detail()}
        loadClaims={async () => analysis()}
        reanalyse={reanalyse}
        pdfViewer={StubViewer}
      />,
    );
    fireEvent.click(
      await screen.findByRole("button", { name: fr.app.viewer.reanalyse.action }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: fr.app.viewer.reanalyse.confirmYes }),
    );
    await waitFor(() => expect(reanalyse).toHaveBeenCalledWith("d1"));
  });

  test("an admin sees no reanalyse control for a document whose upload is not ready", async () => {
    render(
      <DocumentExperience
        documentId="d1"
        role="admin"
        loadDetail={async () => detail({ document: docRecord({ status: "pending" }) })}
        loadClaims={async () =>
          analysis({
            document: docRecord({ status: "pending", analysisStatus: "none" }),
            sentences: [],
          })
        }
        pdfViewer={StubViewer}
      />,
    );
    // Wait for the ready snapshot (the back link renders once loaded).
    await screen.findByText(fr.app.viewer.back);
    expect(
      screen.queryByRole("button", { name: fr.app.viewer.reanalyse.action }),
    ).not.toBeInTheDocument();
  });

  test("a failed analysis shows the error, with a retry only for admins", async () => {
    const failed = () =>
      analysis({
        document: docRecord({
          analysisStatus: "failed",
          analysisError: "interrompu par un redemarrage",
        }),
        sentences: [],
      });

    const { unmount } = render(
      <DocumentExperience
        documentId="d1"
        role="guest"
        loadDetail={async () => detail()}
        loadClaims={async () => failed()}
        pdfViewer={StubViewer}
      />,
    );
    expect(
      await screen.findByText("interrompu par un redemarrage"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: fr.app.viewer.reanalyse.retry }),
    ).not.toBeInTheDocument();
    unmount();

    render(
      <DocumentExperience
        documentId="d1"
        role="admin"
        loadDetail={async () => detail()}
        loadClaims={async () => failed()}
        pdfViewer={StubViewer}
      />,
    );
    expect(
      await screen.findByRole("button", { name: fr.app.viewer.reanalyse.retry }),
    ).toBeInTheDocument();
  });

  test("mid-analysis the progress bar advances and verdicts appear without a refresh", async () => {
    vi.useFakeTimers();
    const loadClaims = vi
      .fn()
      .mockResolvedValueOnce(
        analysis({
          document: docRecord({
            analysisStatus: "analysing",
            sentencesProcessed: 0,
            sentencesTotal: 2,
          }),
          sentences: [],
        }),
      )
      .mockResolvedValue(
        analysis({
          document: docRecord({ analysisStatus: "complete" }),
          sentences: [credibleSentence()],
        }),
      );

    render(
      <DocumentExperience
        documentId="d1"
        role="guest"
        loadDetail={async () => detail()}
        loadClaims={loadClaims}
        pollIntervalMs={2000}
        pdfViewer={StubViewer}
      />,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    // The progress bar is showing and no verdict has landed yet.
    expect(screen.getByRole("progressbar")).toBeInTheDocument();
    expect(
      screen.queryByText(fr.app.claims.verdicts.credible),
    ).not.toBeInTheDocument();

    // The next poll lands the completed analysis; the verdict appears in place.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByText(fr.app.claims.verdicts.credible)).toBeInTheDocument();
  });
});

// A hard refresh resumes from persisted state: the store loads the persisted
// document and claims on mount, so a remount (a page reload) rebuilds the same
// snapshot from the backend rather than any in-memory progress.
describe("useDocumentAnalysis resume-on-mount", () => {
  test("rebuilds the snapshot from persisted state on a fresh mount", async () => {
    const { useDocumentAnalysis } = await import(
      "@/hooks/use-document-analysis"
    );
    const loadDetail = vi.fn().mockResolvedValue(detail());
    const loadClaims = vi
      .fn()
      .mockResolvedValue(
        analysis({ document: docRecord({ analysisStatus: "complete" }) }),
      );
    const { result } = renderHook(() =>
      useDocumentAnalysis({ documentId: "d1", loadDetail, loadClaims }),
    );
    await act(async () => {
      await Promise.resolve();
    });
    await waitFor(() => expect(result.current.snapshot.status).toBe("ready"));
    if (result.current.snapshot.status === "ready") {
      expect(result.current.snapshot.sentences).toHaveLength(2);
      expect(result.current.snapshot.pdfUrl).toBe("https://get/d1.pdf");
    }
  });
});
