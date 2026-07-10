import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import type {
  DocumentUploadError,
  DocumentUploadJobState,
} from "@/hooks/use-document-uploads";
import { DocumentUploadTile } from "./document-upload-tile";

const copy = fr.app.documents.uploader;

function job(state: DocumentUploadJobState, title = "Rapport annuel") {
  return { id: "job-1", title, fileName: `${title}.pdf`, state };
}

describe("DocumentUploadTile", () => {
  test("labels the tile with the document title", () => {
    render(<DocumentUploadTile job={job({ status: "extracting" })} onDismiss={() => {}} />);
    expect(
      screen.getByRole("article", {
        name: formatTemplate(copy.uploadingAria, { title: "Rapport annuel" }),
      }),
    ).toBeInTheDocument();
    expect(screen.getByText("Rapport annuel")).toBeInTheDocument();
  });

  test.each([
    ["extracting", { status: "extracting" } as const, copy.extracting],
    ["requesting", { status: "requesting" } as const, copy.preparing],
    ["confirming", { status: "confirming" } as const, copy.finalizing],
    ["ready", { status: "ready", document: {} } as unknown as DocumentUploadJobState, copy.finalizing],
  ])("shows the working copy for %s", (_name, state, expected) => {
    render(<DocumentUploadTile job={job(state)} onDismiss={() => {}} />);
    expect(screen.getByText(expected)).toBeInTheDocument();
  });

  test("renders the upload progress as a bar width", () => {
    const { container } = render(
      <DocumentUploadTile job={job({ status: "uploading", progress: 0.6 })} onDismiss={() => {}} />,
    );
    const bar = container.querySelector('[style*="width: 60%"]');
    expect(bar).not.toBeNull();
  });

  test.each<[string, DocumentUploadError, string]>([
    ["unsupported", { kind: "unsupported" }, copy.errors.unsupported],
    ["scanned", { kind: "scanned" }, copy.errors.scanned],
    ["tooLong", { kind: "tooLong", max: 1500 }, formatTemplate(copy.errors.tooLong, { max: 1500 })],
    ["failed with a message", { kind: "failed", message: "boom" }, "boom"],
    ["failed without a message", { kind: "failed", message: null }, copy.errors.failed],
  ])("renders the %s error as an alert", (_name, error, expected) => {
    render(<DocumentUploadTile job={job({ status: "error", error })} onDismiss={() => {}} />);
    expect(screen.getByRole("alert")).toHaveTextContent(expected);
  });

  test("dismiss invokes the callback with the job id", async () => {
    const onDismiss = vi.fn();
    render(<DocumentUploadTile job={job({ status: "extracting" })} onDismiss={onDismiss} />);
    await userEvent.click(screen.getByRole("button", { name: copy.dismiss }));
    expect(onDismiss).toHaveBeenCalledWith("job-1");
  });
});
