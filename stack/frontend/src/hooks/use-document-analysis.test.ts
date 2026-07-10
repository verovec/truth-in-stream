import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type {
  DocumentAnalysis,
  DocumentDetail,
  DocumentRecord,
} from "@/lib/documents/api";
import { ApiError } from "@/lib/http";
import { useDocumentAnalysis } from "./use-document-analysis";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

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
    sentencesTotal: 3,
    sentencesProcessed: 1,
    analysisRuns: 0,
    analyzedAt: "",
    createdAt: "2026-07-09T09:00:00Z",
    updatedAt: "2026-07-09T09:00:00Z",
    ...over,
  };
}

function detail(over: Partial<DocumentDetail> = {}): DocumentDetail {
  return { document: docRecord(), pdfUrl: "https://get/d1.pdf", ...over };
}

function analysis(over: Partial<DocumentAnalysis> = {}): DocumentAnalysis {
  return { document: docRecord(), sentences: [], ...over };
}

// flush advances fake timers by ms and drains the promise chains the ticks
// schedule; 0 flushes the initial load's microtasks with no timer pending.
async function flush(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

describe("useDocumentAnalysis", () => {
  test("loads the PDF URL and the sentences once, exposing a ready snapshot", async () => {
    vi.useFakeTimers();
    const loadDetail = vi.fn().mockResolvedValue(detail());
    const loadClaims = vi.fn().mockResolvedValue(
      analysis({
        document: docRecord({ analysisStatus: "complete", sentencesProcessed: 3 }),
        sentences: [
          { seq: 0, page: 1, text: "Une.", occurrence: 1, skipReason: "", claims: [] },
        ],
      }),
    );

    const { result } = renderHook(() =>
      useDocumentAnalysis({ documentId: "d1", loadDetail, loadClaims, pollIntervalMs: 2000 }),
    );
    await flush(0);

    expect(result.current.snapshot.status).toBe("ready");
    if (result.current.snapshot.status === "ready") {
      expect(result.current.snapshot.pdfUrl).toBe("https://get/d1.pdf");
      expect(result.current.snapshot.sentences).toHaveLength(1);
    }
    expect(loadDetail).toHaveBeenCalledTimes(1);
    // A completed document is not polled.
    await flush(6000);
    expect(loadClaims).toHaveBeenCalledTimes(1);
  });

  test("polls the claims while analysing and stops once terminal, never refetching the PDF", async () => {
    vi.useFakeTimers();
    const loadDetail = vi.fn().mockResolvedValue(detail());
    const loadClaims = vi
      .fn()
      .mockResolvedValueOnce(analysis({ document: docRecord({ sentencesProcessed: 1 }) }))
      .mockResolvedValueOnce(analysis({ document: docRecord({ sentencesProcessed: 2 }) }))
      .mockResolvedValue(
        analysis({ document: docRecord({ analysisStatus: "complete", sentencesProcessed: 3 }) }),
      );

    const { result } = renderHook(() =>
      useDocumentAnalysis({ documentId: "d1", loadDetail, loadClaims, pollIntervalMs: 2000 }),
    );
    await flush(0);
    expect(loadClaims).toHaveBeenCalledTimes(1);

    await flush(2000);
    expect(loadClaims).toHaveBeenCalledTimes(2);

    await flush(2000);
    expect(loadClaims).toHaveBeenCalledTimes(3);
    if (result.current.snapshot.status === "ready") {
      expect(result.current.snapshot.document.analysisStatus).toBe("complete");
    }

    // Terminal: no further polls no matter how much time passes.
    await flush(10000);
    expect(loadClaims).toHaveBeenCalledTimes(3);
    expect(loadDetail).toHaveBeenCalledTimes(1);
  });

  test("backs off on a transient poll failure and recovers", async () => {
    vi.useFakeTimers();
    const loadDetail = vi.fn().mockResolvedValue(detail());
    const loadClaims = vi
      .fn()
      .mockResolvedValueOnce(analysis()) // initial, analysing
      .mockRejectedValueOnce(new Error("network down")) // first poll fails
      .mockResolvedValue(analysis({ document: docRecord({ sentencesProcessed: 2 }) }));

    renderHook(() =>
      useDocumentAnalysis({ documentId: "d1", loadDetail, loadClaims, pollIntervalMs: 2000 }),
    );
    await flush(0);
    expect(loadClaims).toHaveBeenCalledTimes(1);

    // First poll at the base interval fails.
    await flush(2000);
    expect(loadClaims).toHaveBeenCalledTimes(2);

    // The retry is backed off to 2x the base, so nothing fires at the base delay.
    await flush(2000);
    expect(loadClaims).toHaveBeenCalledTimes(2);

    // It recovers at the backed-off delay.
    await flush(2000);
    expect(loadClaims).toHaveBeenCalledTimes(3);
  });

  test("stops polling and surfaces an error on a permanent client failure", async () => {
    vi.useFakeTimers();
    const loadDetail = vi.fn().mockResolvedValue(detail());
    const loadClaims = vi
      .fn()
      .mockResolvedValueOnce(analysis()) // initial, analysing
      .mockRejectedValue(new ApiError("unknown document", 404));

    const { result } = renderHook(() =>
      useDocumentAnalysis({ documentId: "d1", loadDetail, loadClaims, pollIntervalMs: 2000 }),
    );
    await flush(0);
    expect(loadClaims).toHaveBeenCalledTimes(1);

    // The first poll 404s: the loop stops and surfaces the error.
    await flush(2000);
    expect(loadClaims).toHaveBeenCalledTimes(2);
    expect(result.current.snapshot.status).toBe("error");

    // No further polls, no infinite retry.
    await flush(60000);
    expect(loadClaims).toHaveBeenCalledTimes(2);
  });

  test("refresh re-fetches the claims and re-arms polling without refetching the PDF", async () => {
    vi.useFakeTimers();
    const loadDetail = vi.fn().mockResolvedValue(detail());
    const loadClaims = vi
      .fn()
      .mockResolvedValueOnce(analysis({ document: docRecord({ analysisStatus: "complete" }) }))
      .mockResolvedValue(analysis({ document: docRecord({ analysisStatus: "analysing" }) }));

    const { result } = renderHook(() =>
      useDocumentAnalysis({ documentId: "d1", loadDetail, loadClaims, pollIntervalMs: 2000 }),
    );
    await flush(0);
    expect(loadClaims).toHaveBeenCalledTimes(1);

    // Complete: not polling.
    await flush(4000);
    expect(loadClaims).toHaveBeenCalledTimes(1);

    // A reanalyse fired: refresh observes the analysing transition and re-arms.
    act(() => result.current.refresh());
    await flush(0);
    expect(loadClaims).toHaveBeenCalledTimes(2);
    await flush(2000);
    expect(loadClaims).toHaveBeenCalledTimes(3);

    // The PDF is fetched exactly once across the whole lifecycle.
    expect(loadDetail).toHaveBeenCalledTimes(1);
  });
});
