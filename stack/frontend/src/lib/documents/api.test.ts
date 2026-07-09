import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import {
  ingestExtraction,
  listDocuments,
  requestDocumentUpload,
} from "./api";

afterEach(() => vi.restoreAllMocks());

const documentWire = {
  id: "doc-1",
  title: "Rapport",
  status: "ready",
  analysis_status: "complete",
  content_type: "application/pdf",
  size_bytes: 2048,
  page_count: 3,
  sentences_total: 12,
  sentences_processed: 12,
  analysis_runs: 1,
  analyzed_at: "2026-07-09T10:00:00Z",
  created_at: "2026-07-09T09:00:00Z",
  updated_at: "2026-07-09T10:00:00Z",
};

describe("listDocuments", () => {
  test("maps the wire rows, including verdict summary counts, to camelCase", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/documents"),
        responses: [
          json(200, {
            documents: [{ ...documentWire, credible_claims: 4, disputed_claims: 2 }],
          }),
        ],
      },
    ]);
    const docs = await listDocuments();
    expect(docs).toHaveLength(1);
    expect(docs[0]).toMatchObject({
      id: "doc-1",
      title: "Rapport",
      status: "ready",
      analysisStatus: "complete",
      pageCount: 3,
      sentencesTotal: 12,
      sentencesProcessed: 12,
      analysisRuns: 1,
      credibleClaims: 4,
      disputedClaims: 2,
    });
  });

  test("an absent documents array yields an empty list", async () => {
    stubBackend([{ match: (url) => url.endsWith("/api/documents"), responses: [json(200, {})] }]);
    expect(await listDocuments()).toEqual([]);
  });

  test("surfaces a backend error", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/documents"),
        responses: [json(500, { error: "boom" })],
      },
    ]);
    await expect(listDocuments()).rejects.toThrow("boom");
  });
});

describe("requestDocumentUpload", () => {
  test("posts the snake_case body and maps the ticket", async () => {
    let sentBody: unknown = null;
    vi.spyOn(globalThis, "fetch").mockImplementation((_input, init) => {
      sentBody = init?.body ? JSON.parse(String(init.body)) : null;
      return Promise.resolve(
        new Response(
          JSON.stringify({
            document_id: "doc-1",
            object_key: "documents/doc-1/original.pdf",
            status: "pending",
            upload: { url: "https://put/doc-1", method: "PUT", headers: { "If-None-Match": ["*"] } },
            max_sentences: 1500,
          }),
          { status: 201 },
        ),
      );
    });

    const ticket = await requestDocumentUpload({
      title: "Rapport",
      contentType: "application/pdf",
      sizeBytes: 2048,
    });
    expect(sentBody).toEqual({
      title: "Rapport",
      content_type: "application/pdf",
      size_bytes: 2048,
    });
    expect(ticket).toMatchObject({
      documentId: "doc-1",
      objectKey: "documents/doc-1/original.pdf",
      status: "pending",
      maxSentences: 1500,
    });
    expect(ticket.upload).toMatchObject({ url: "https://put/doc-1", method: "PUT" });
  });
});

describe("ingestExtraction", () => {
  test("posts the extraction and returns the ready document", async () => {
    let sentBody: unknown = null;
    vi.spyOn(globalThis, "fetch").mockImplementation((_input, init) => {
      sentBody = init?.body ? JSON.parse(String(init.body)) : null;
      return Promise.resolve(
        new Response(JSON.stringify({ ...documentWire, status: "ready", analysis_status: "analysing" }), {
          status: 200,
        }),
      );
    });

    const doc = await ingestExtraction("doc-1", {
      pageCount: 2,
      sentences: [
        { seq: 0, page: 1, text: "Une.", occurrence: 1 },
        { seq: 1, page: 2, text: "Deux.", occurrence: 1 },
      ],
    });
    expect(sentBody).toEqual({
      page_count: 2,
      sentences: [
        { seq: 0, page: 1, text: "Une.", occurrence: 1 },
        { seq: 1, page: 2, text: "Deux.", occurrence: 1 },
      ],
    });
    expect(doc).toMatchObject({ id: "doc-1", status: "ready", analysisStatus: "analysing" });
  });

  test("surfaces a conflict from a stale extraction", async () => {
    stubBackend([
      {
        match: (url) => url.includes("/api/documents/doc-1/extraction"),
        responses: [json(409, { error: "document is not pending extraction" })],
      },
    ]);
    await expect(
      ingestExtraction("doc-1", { pageCount: 1, sentences: [{ seq: 0, page: 1, text: "x", occurrence: 1 }] }),
    ).rejects.toThrow("document is not pending extraction");
  });
});
