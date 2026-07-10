import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import { ApiError } from "@/lib/http";
import {
  getDocument,
  getDocumentClaims,
  ingestExtraction,
  isAcceptedDocumentFile,
  listDocuments,
  reanalyseDocument,
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

describe("isAcceptedDocumentFile", () => {
  test.each([
    ["a declared PDF", "rapport.pdf", "application/pdf", true],
    ["an empty MIME with a .pdf name", "rapport.pdf", "", true],
    ["a generic binary MIME with a .pdf name", "RAPPORT.PDF", "application/octet-stream", true],
    ["an empty MIME without a .pdf name", "rapport", "", false],
    ["a declared non-PDF even with a .pdf name", "rapport.pdf", "image/png", false],
  ])("%s -> %s", (_name, fileName, type, expected) => {
    expect(isAcceptedDocumentFile(fileName, type)).toBe(expected);
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

describe("getDocument", () => {
  test("maps the metadata and surfaces the presigned PDF URL", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/documents/doc-1"),
        responses: [
          json(200, {
            ...documentWire,
            pdf: { url: "https://get/doc-1.pdf", method: "GET", headers: {} },
          }),
        ],
      },
    ]);
    const detail = await getDocument("doc-1");
    expect(detail.document).toMatchObject({ id: "doc-1", pageCount: 3 });
    expect(detail.pdfUrl).toBe("https://get/doc-1.pdf");
  });

  test("a document with no presigned PDF yields a null URL", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/documents/doc-1"),
        responses: [json(200, { ...documentWire, status: "pending" })],
      },
    ]);
    expect((await getDocument("doc-1")).pdfUrl).toBeNull();
  });
});

describe("getDocumentClaims", () => {
  test("maps sentences and their claims onto the shared verdict shape", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/documents/doc-1/claims"),
        responses: [
          json(200, {
            document: documentWire,
            sentences: [
              {
                seq: 0,
                page: 1,
                text: "Le chômage a baissé.",
                occurrence: 1,
                claims: [
                  {
                    id: "row-1",
                    claim_id: "c-1",
                    text: "Le chômage a baissé.",
                    status: "verified",
                    source: "verified",
                    verdict: "credible",
                    basis: "evidence",
                    confidence: 0.82,
                    rationale: "Corroboré.",
                    citations: [
                      {
                        kind: "claim",
                        claim: "Baisse du chômage",
                        verdict: "corroborates",
                        sources: [{ title: "INSEE", url: "https://insee.fr" }],
                        similarity: 0.9,
                      },
                    ],
                  },
                ],
              },
              {
                seq: 1,
                page: 1,
                text: "Bonjour.",
                occurrence: 1,
                skip_reason: "not_a_claim",
                claims: [],
              },
            ],
          }),
        ],
      },
    ]);
    const analysis = await getDocumentClaims("doc-1");
    expect(analysis.document).toMatchObject({ id: "doc-1", analysisStatus: "complete" });
    expect(analysis.sentences).toHaveLength(2);

    const [first, second] = analysis.sentences;
    expect(first).toMatchObject({ seq: 0, page: 1, skipReason: "" });
    expect(first?.claims[0]).toMatchObject({
      claimId: "c-1",
      status: "verified",
      verdict: "credible",
      confidence: 0.82,
    });
    expect(first?.claims[0]?.matches?.[0]).toMatchObject({
      kind: "claim",
      claim: "Baisse du chômage",
      verdict: "corroborates",
    });

    expect(second).toMatchObject({ seq: 1, skipReason: "not_a_claim", claims: [] });
  });

  test("an absent sentences array yields an empty list", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/documents/doc-1/claims"),
        responses: [json(200, { document: documentWire })],
      },
    ]);
    expect((await getDocumentClaims("doc-1")).sentences).toEqual([]);
  });
});

describe("reanalyseDocument", () => {
  test("resolves on a 202 accepted", async () => {
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/documents/doc-1/reanalyse") && init?.method === "POST",
        responses: [() => new Response(null, { status: 202 })],
      },
    ]);
    await expect(reanalyseDocument("doc-1")).resolves.toBeUndefined();
  });

  test("surfaces a 409 concurrent-run conflict as an ApiError with its status", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/documents/doc-1/reanalyse"),
        responses: [json(409, { error: "analysis is already in progress" })],
      },
    ]);
    await expect(reanalyseDocument("doc-1")).rejects.toMatchObject({
      status: 409,
    });
    await expect(reanalyseDocument("doc-1")).rejects.toBeInstanceOf(ApiError);
  });
});
