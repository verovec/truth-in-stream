import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import { ScannedPdfError, type ExtractionResult } from "@/lib/pdf/extract";
import type { PutUploader } from "@/lib/video/upload";
import { useDocumentUploads } from "./use-document-uploads";

function pdf(name = "rapport.pdf") {
  return new File(["x".repeat(20)], name, { type: "application/pdf" });
}

function extraction(sentenceCount: number): ExtractionResult {
  return {
    pageCount: 1,
    sentences: Array.from({ length: sentenceCount }, (_v, i) => ({
      seq: i,
      page: 1,
      text: `Phrase ${i}.`,
      occurrence: 1,
    })),
  };
}

function uploadRoutes(maxSentences = 1500, extractionStatus = 200) {
  return [
    {
      match: (u: string, i?: RequestInit) =>
        u.endsWith("/api/documents/uploads") && i?.method === "POST",
      responses: [
        json(201, {
          document_id: "doc-9",
          object_key: "documents/doc-9/original.pdf",
          status: "pending",
          upload: { url: "https://storage/put", method: "PUT", headers: { "If-None-Match": ["*"] } },
          max_sentences: maxSentences,
        }),
      ],
    },
    {
      match: (u: string) => u.includes("/api/documents/doc-9/extraction"),
      responses: [
        json(extractionStatus, {
          id: "doc-9",
          title: "rapport",
          status: "ready",
          analysis_status: "analysing",
          content_type: "application/pdf",
          size_bytes: 20,
          page_count: 1,
          sentences_total: 2,
          sentences_processed: 0,
          analysis_runs: 0,
          created_at: "2026-07-09T10:00:00Z",
          updated_at: "2026-07-09T10:00:00Z",
        }),
      ],
    },
  ];
}

const resolvingUploader: PutUploader = async (_p, _f, onProgress) => {
  onProgress(5, 10);
};

afterEach(() => vi.restoreAllMocks());

describe("useDocumentUploads", () => {
  test("extracts, requests, uploads, and confirms a PDF to a ready record", async () => {
    stubBackend(uploadRoutes());
    const onUploaded = vi.fn();
    const { result } = renderHook(() =>
      useDocumentUploads({
        uploader: resolvingUploader,
        extractor: async () => extraction(2),
        onUploaded,
      }),
    );

    act(() => result.current.startUploads([pdf("Mon Rapport.pdf")]));
    expect(result.current.jobs).toHaveLength(1);
    expect(result.current.jobs[0].title).toBe("Mon Rapport");
    expect(result.current.jobs[0].state.status).toBe("extracting");

    await waitFor(() => expect(result.current.jobs[0].state.status).toBe("ready"));
    expect(onUploaded).toHaveBeenCalledWith(expect.objectContaining({ id: "doc-9", status: "ready" }));
  });

  test("rejects a non-PDF locally without any server call", async () => {
    const fetchSpy = stubBackend([]);
    const { result } = renderHook(() =>
      useDocumentUploads({ uploader: resolvingUploader, extractor: async () => extraction(1) }),
    );
    act(() => result.current.startUploads([new File(["x"], "clip.mp4", { type: "video/mp4" })]));
    expect(result.current.jobs[0].state).toEqual({ status: "error", error: { kind: "unsupported" } });
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  test("rejects a scanned PDF before anything reaches the server", async () => {
    const fetchSpy = stubBackend([]);
    const { result } = renderHook(() =>
      useDocumentUploads({
        uploader: resolvingUploader,
        extractor: async () => {
          throw new ScannedPdfError();
        },
      }),
    );
    act(() => result.current.startUploads([pdf()]));
    await waitFor(() =>
      expect(result.current.jobs[0].state).toEqual({ status: "error", error: { kind: "scanned" } }),
    );
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  test("rejects a document over the sentence cap before the upload PUT", async () => {
    stubBackend(uploadRoutes(2));
    let uploaded = false;
    const uploader: PutUploader = async () => {
      uploaded = true;
    };
    const { result } = renderHook(() =>
      useDocumentUploads({ uploader, extractor: async () => extraction(5) }),
    );
    act(() => result.current.startUploads([pdf()]));
    await waitFor(() => expect(result.current.jobs[0].state.status).toBe("error"));
    expect(result.current.jobs[0].state).toEqual({
      status: "error",
      error: { kind: "tooLong", max: 2 },
    });
    expect(uploaded).toBe(false);
  });

  test("surfaces a backend failure on the extraction POST", async () => {
    stubBackend(uploadRoutes(1500, 409));
    const { result } = renderHook(() =>
      useDocumentUploads({ uploader: resolvingUploader, extractor: async () => extraction(2) }),
    );
    act(() => result.current.startUploads([pdf()]));
    await waitFor(() => expect(result.current.jobs[0].state.status).toBe("error"));
    const state = result.current.jobs[0].state;
    expect(state.status === "error" && state.error.kind).toBe("failed");
  });

  test("dismiss aborts an in-flight job and removes it", async () => {
    stubBackend(uploadRoutes());
    // A never-resolving uploader keeps the job in flight so dismiss can cancel it.
    const uploader: PutUploader = () => new Promise(() => {});
    const { result } = renderHook(() =>
      useDocumentUploads({ uploader, extractor: async () => extraction(2) }),
    );
    act(() => result.current.startUploads([pdf()]));
    await waitFor(() => expect(result.current.jobs[0].state.status).toBe("uploading"));
    const id = result.current.jobs[0].id;
    act(() => result.current.dismiss(id));
    expect(result.current.jobs).toHaveLength(0);
  });
});
