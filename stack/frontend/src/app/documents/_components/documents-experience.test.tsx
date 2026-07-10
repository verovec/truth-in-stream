import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { LibraryDocument } from "@/lib/documents/api";
import type { ExtractionResult } from "@/lib/pdf/extract";
import type { PutUploader } from "@/lib/video/upload";
import { DocumentsExperience } from "./documents-experience";

afterEach(() => vi.restoreAllMocks());

function libraryDoc(over: Partial<LibraryDocument> = {}): LibraryDocument {
  return {
    id: "doc-1",
    title: "Rapport annuel",
    status: "ready",
    analysisStatus: "complete",
    analysisError: "",
    contentType: "application/pdf",
    sizeBytes: 2048,
    pageCount: 3,
    sentencesTotal: 12,
    sentencesProcessed: 12,
    analysisRuns: 1,
    analyzedAt: "2026-07-09T10:00:00Z",
    createdAt: "2026-07-09T09:00:00Z",
    updatedAt: "2026-07-09T10:00:00Z",
    credibleClaims: 4,
    disputedClaims: 2,
    ...over,
  };
}

describe("DocumentsExperience", () => {
  test("loads and renders documents with their verdict counts", async () => {
    render(
      <DocumentsExperience loadDocuments={async () => [libraryDoc()]} />,
    );
    expect(await screen.findByText("Rapport annuel")).toBeInTheDocument();
    expect(screen.getByText(/4 fiables/)).toBeInTheDocument();
    expect(screen.getByText(/2 contestés/)).toBeInTheDocument();
    expect(screen.getByText(fr.app.documents.analysis.complete)).toBeInTheDocument();
  });

  test("shows the admin drop zone only to admins", async () => {
    const { rerender } = render(
      <DocumentsExperience role="guest" loadDocuments={async () => []} />,
    );
    await screen.findByText(fr.app.documents.empty);
    expect(screen.queryByTestId("document-upload-zone")).not.toBeInTheDocument();

    rerender(<DocumentsExperience role="admin" loadDocuments={async () => []} />);
    await screen.findByText(fr.app.documents.emptyAdmin);
    expect(screen.getByTestId("document-upload-zone")).toBeInTheDocument();
  });

  test("surfaces a load error with a retry that refetches", async () => {
    let calls = 0;
    render(
      <DocumentsExperience
        loadDocuments={async () => {
          calls += 1;
          if (calls === 1) {
            throw new Error("network down");
          }
          return [libraryDoc()];
        }}
      />,
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("network down");
    fireEvent.click(screen.getByRole("button", { name: fr.app.documents.retry }));
    expect(await screen.findByText("Rapport annuel")).toBeInTheDocument();
  });

  test("an admin uploading a PDF sees it become a document card", async () => {
    stubBackend([
      {
        match: (u, i) => u.endsWith("/api/documents/uploads") && i?.method === "POST",
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
      // The settling poll re-lists the catalog; return the doc analysed.
      {
        match: (u) => u.endsWith("/api/documents"),
        responses: [json(200, { documents: [] })],
      },
    ]);
    const extractor = async (): Promise<ExtractionResult> => ({
      pageCount: 1,
      sentences: [{ seq: 0, page: 1, text: "Une phrase.", occurrence: 1 }],
    });
    const uploader: PutUploader = async (_p, _f, onProgress) => onProgress(10, 10);

    render(
      <DocumentsExperience
        role="admin"
        loadDocuments={async () => []}
        pollDocuments={async () => []}
        extractor={extractor}
        uploader={uploader}
      />,
    );

    const input = await screen.findByLabelText(fr.app.documents.uploader.inputAria);
    const file = new File(["x".repeat(20)], "Nouveau.pdf", { type: "application/pdf" });
    await userEvent.upload(input, file);

    // The upload drives to a ready record, which appears as a real document card.
    expect(await screen.findByText("Nouveau")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByText(fr.app.documents.analysis.analysing)).toBeInTheDocument(),
    );
  });

  test("keeps upload tiles visible while the catalog load has failed", async () => {
    // A hanging extractor keeps the job in its first state so the tile stays on
    // screen; the catalog below shows its error at the same time.
    const extractor = () => new Promise<ExtractionResult>(() => {});
    render(
      <DocumentsExperience
        role="admin"
        loadDocuments={async () => {
          throw new Error("network down");
        }}
        extractor={extractor}
        uploader={async () => {}}
      />,
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("network down");

    const input = screen.getByLabelText(fr.app.documents.uploader.inputAria);
    const file = new File(["x".repeat(20)], "Rapport.pdf", { type: "application/pdf" });
    await userEvent.upload(input, file);

    // The upload feedback appears even though the list failed to load, and the
    // load error is still shown.
    expect(
      await screen.findByText(fr.app.documents.uploader.extracting),
    ).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("network down");
  });

  test("stops polling a stuck document after the no-progress idle bound", async () => {
    // A document stuck at "analysing" that never advances: the poll returns the
    // same signature every tick, so idle ticks accrue and polling halts.
    const stuck = libraryDoc({ id: "doc-stuck", analysisStatus: "analysing", sentencesProcessed: 3 });
    const poll = vi.fn(async () => [stuck]);
    render(
      <DocumentsExperience
        loadDocuments={async () => [stuck]}
        pollDocuments={poll}
        pollIntervalMs={5}
        maxIdlePolls={2}
      />,
    );
    await screen.findByText("Rapport annuel");
    // Once no progress is seen for maxIdlePolls ticks the interval is cleared, so
    // the call count stops growing.
    await waitFor(() => expect(poll.mock.calls.length).toBeGreaterThanOrEqual(3));
    const stopped = poll.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(poll).toHaveBeenCalledTimes(stopped);
  });

  test("keeps polling while a document is still making progress", async () => {
    // sentencesProcessed climbs each tick, so the progress signature changes and
    // the idle counter never reaches the bound - polling continues well past it.
    let processed = 0;
    const poll = vi.fn(async () => {
      processed += 1;
      return [
        libraryDoc({
          id: "doc-progress",
          analysisStatus: "analysing",
          sentencesProcessed: processed,
        }),
      ];
    });
    render(
      <DocumentsExperience
        loadDocuments={async () => [
          libraryDoc({ id: "doc-progress", analysisStatus: "analysing", sentencesProcessed: 0 }),
        ]}
        pollDocuments={poll}
        pollIntervalMs={5}
        maxIdlePolls={2}
      />,
    );
    await screen.findByText("Rapport annuel");
    // Far more polls than maxIdlePolls, because each tick observes progress.
    await waitFor(() => expect(poll.mock.calls.length).toBeGreaterThan(6));
  });

  test("preserves an optimistic upload card when the initial load resolves later", async () => {
    stubBackend([
      {
        match: (u, i) => u.endsWith("/api/documents/uploads") && i?.method === "POST",
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
            analysis_status: "complete",
            content_type: "application/pdf",
            size_bytes: 20,
            page_count: 1,
            sentences_total: 1,
            sentences_processed: 1,
            analysis_runs: 1,
            created_at: "2026-07-09T11:00:00Z",
            updated_at: "2026-07-09T11:00:00Z",
          }),
        ],
      },
    ]);
    // The initial catalog load is deferred so the upload's onUploaded fires while
    // it is still in flight - the exact race that a blind setDocuments(loaded)
    // would lose.
    let resolveLoad: (docs: LibraryDocument[]) => void = () => {};
    const loadDocuments = () =>
      new Promise<LibraryDocument[]>((resolve) => {
        resolveLoad = resolve;
      });
    const extractor = async (): Promise<ExtractionResult> => ({
      pageCount: 1,
      sentences: [{ seq: 0, page: 1, text: "Une phrase.", occurrence: 1 }],
    });
    const uploader: PutUploader = async (_p, _f, onProgress) => onProgress(10, 10);

    render(
      <DocumentsExperience
        role="admin"
        loadDocuments={loadDocuments}
        pollDocuments={async () => []}
        extractor={extractor}
        uploader={uploader}
      />,
    );

    const input = await screen.findByLabelText(fr.app.documents.uploader.inputAria);
    const file = new File(["x".repeat(20)], "Nouveau.pdf", { type: "application/pdf" });
    await userEvent.upload(input, file);
    // Wait until the upload has confirmed (its tile is pruned) before the load
    // resolves, so the optimistic card is the only trace of the new document.
    await waitFor(() =>
      expect(
        screen.queryByText(fr.app.documents.uploader.finalizing),
      ).not.toBeInTheDocument(),
    );

    // The backend list, captured before the upload existed, comes back empty.
    act(() => resolveLoad([]));
    expect(await screen.findByText("Nouveau")).toBeInTheDocument();
  });

  test("rejects a scanned PDF inline without listing a card", async () => {
    const { ScannedPdfError } = await import("@/lib/pdf/extract");
    stubBackend([
      { match: (u) => u.endsWith("/api/documents"), responses: [json(200, { documents: [] })] },
    ]);
    render(
      <DocumentsExperience
        role="admin"
        loadDocuments={async () => []}
        extractor={async () => {
          throw new ScannedPdfError();
        }}
        uploader={async () => {}}
      />,
    );
    const input = await screen.findByLabelText(fr.app.documents.uploader.inputAria);
    const file = new File(["x"], "scan.pdf", { type: "application/pdf" });
    await userEvent.upload(input, file);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(fr.app.documents.uploader.errors.scanned);
  });
});
