import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { ExtractionResult } from "@/lib/pdf/extract";
import type { PutUploader } from "@/lib/video/upload";
import { BackofficeDocumentsSection } from "./backoffice-documents-section";

const copy = fr.app.documents.uploader;

afterEach(() => vi.restoreAllMocks());

describe("BackofficeDocumentsSection", () => {
  test("renders the drop zone and the guidance hint", () => {
    render(<BackofficeDocumentsSection />);
    expect(screen.getByTestId("document-upload-zone")).toBeInTheDocument();
    expect(
      screen.getByText(fr.app.backoffice.documents.hint),
    ).toBeInTheDocument();
  });

  test("an uploaded PDF drives an in-flight tile through to confirmation", async () => {
    // The section has no catalog: a confirmed document lands on /documents, so
    // the only trace here is the in-flight tile, which is pruned once the upload
    // confirms (the ready job leaves the list).
    stubBackend([
      {
        match: (u, i) =>
          u.endsWith("/api/documents/uploads") && i?.method === "POST",
        responses: [
          json(201, {
            document_id: "doc-9",
            object_key: "documents/doc-9/original.pdf",
            status: "pending",
            upload: { url: "https://storage/put", method: "PUT", headers: {} },
            max_sentences: 1500,
          }),
        ],
      },
      {
        match: (u) => u.includes("/api/documents/doc-9/extraction"),
        responses: [
          json(200, {
            id: "doc-9",
            title: "Nouveau",
            status: "ready",
            analysis_status: "analysing",
            content_type: "application/pdf",
            size_bytes: 20,
            page_count: 1,
            sentences_total: 1,
            sentences_processed: 0,
            analysis_runs: 0,
            created_at: "2026-07-09T11:00:00Z",
            updated_at: "2026-07-09T11:00:00Z",
          }),
        ],
      },
    ]);
    // A deferred extractor freezes the job at "extracting" so the in-flight tile
    // is observable before the rest of the chain runs to completion.
    let resolveExtraction: (result: ExtractionResult) => void = () => {};
    const extractor = () =>
      new Promise<ExtractionResult>((resolve) => {
        resolveExtraction = resolve;
      });
    const uploader: PutUploader = async (_p, _f, onProgress) =>
      onProgress(10, 10);

    render(
      <BackofficeDocumentsSection extractor={extractor} uploader={uploader} />,
    );

    const input = screen.getByLabelText(copy.inputAria);
    const file = new File(["x".repeat(20)], "Nouveau.pdf", {
      type: "application/pdf",
    });
    await userEvent.upload(input, file);

    // The tile shows the file being extracted while the extractor is pending.
    expect(await screen.findByText("Nouveau")).toBeInTheDocument();
    expect(screen.getByText(copy.extracting)).toBeInTheDocument();

    // Once extraction resolves the chain requests, uploads, and confirms; a
    // confirmed (ready) job is pruned, so the tile leaves the section entirely.
    resolveExtraction({
      pageCount: 1,
      sentences: [{ seq: 0, page: 1, text: "Une phrase.", occurrence: 1 }],
    });
    await waitFor(() =>
      expect(screen.queryByText("Nouveau")).not.toBeInTheDocument(),
    );
  });

  test("a scanned PDF is rejected inline without any server call", async () => {
    const { ScannedPdfError } = await import("@/lib/pdf/extract");
    // No route is scripted: a scanned PDF must be rejected before any fetch, so
    // the empty stub throws loudly if the upstream chain reaches the network.
    stubBackend([]);
    render(
      <BackofficeDocumentsSection
        extractor={async () => {
          throw new ScannedPdfError();
        }}
        uploader={async () => {}}
      />,
    );
    const input = screen.getByLabelText(copy.inputAria);
    const file = new File(["x"], "scan.pdf", { type: "application/pdf" });
    await userEvent.upload(input, file);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(copy.errors.scanned);
  });
});
