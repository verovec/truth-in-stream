import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { DocumentUploader } from "./document-uploader";

describe("DocumentUploader", () => {
  test("emits the chosen files and only accepts PDFs", async () => {
    const onFiles = vi.fn();
    render(<DocumentUploader onFiles={onFiles} />);
    const input = screen.getByLabelText(fr.app.documents.uploader.inputAria);
    expect(input).toHaveAttribute("accept", "application/pdf");
    const file = new File(["x"], "rapport.pdf", { type: "application/pdf" });
    await userEvent.upload(input, file);
    expect(onFiles).toHaveBeenCalledWith([file]);
  });

  test("shows the prompt and the supported-format hint", () => {
    render(<DocumentUploader onFiles={vi.fn()} />);
    expect(screen.getByText(fr.app.documents.uploader.prompt)).toBeInTheDocument();
    expect(screen.getByText(fr.app.documents.uploader.formats)).toBeInTheDocument();
  });
});
