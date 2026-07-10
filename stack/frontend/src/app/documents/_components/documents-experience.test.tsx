import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { LibraryDocument } from "@/lib/documents/api";
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
    render(<DocumentsExperience loadDocuments={async () => [libraryDoc()]} />);
    expect(await screen.findByText("Rapport annuel")).toBeInTheDocument();
    expect(screen.getByText(/4 fiables/)).toBeInTheDocument();
    expect(screen.getByText(/2 contestés/)).toBeInTheDocument();
    expect(
      screen.getByText(fr.app.documents.analysis.complete),
    ).toBeInTheDocument();
  });

  test("is consumption-only: no uploader and one role-independent empty state", async () => {
    // Ingestion moved to the backoffice, so the documents page never renders the
    // upload zone and shows a single empty state to every authenticated reader.
    render(<DocumentsExperience loadDocuments={async () => []} />);
    await screen.findByText(fr.app.documents.empty);
    expect(screen.queryByTestId("document-upload-zone")).not.toBeInTheDocument();
    expect(
      screen.queryByLabelText(fr.app.documents.uploader.inputAria),
    ).not.toBeInTheDocument();
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
    fireEvent.click(
      screen.getByRole("button", { name: fr.app.documents.retry }),
    );
    expect(await screen.findByText("Rapport annuel")).toBeInTheDocument();
  });

  test("stops polling a stuck document after the no-progress idle bound", async () => {
    // A document stuck at "analysing" that never advances: the poll returns the
    // same signature every tick, so idle ticks accrue and polling halts.
    const stuck = libraryDoc({
      id: "doc-stuck",
      analysisStatus: "analysing",
      sentencesProcessed: 3,
    });
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
    await waitFor(() =>
      expect(poll.mock.calls.length).toBeGreaterThanOrEqual(3),
    );
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
          libraryDoc({
            id: "doc-progress",
            analysisStatus: "analysing",
            sentencesProcessed: 0,
          }),
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
});
