import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { VideoUploader } from "./video-uploader";

function mp4(name = "clip.mp4") {
  return new File(["x".repeat(10)], name, { type: "video/mp4" });
}

describe("VideoUploader", () => {
  test("shows the drop prompt and the accepted formats", () => {
    render(<VideoUploader onFiles={vi.fn()} />);

    expect(screen.getByText(fr.app.uploader.prompt)).toBeInTheDocument();
    expect(screen.getByText(fr.app.uploader.formats)).toBeInTheDocument();
  });

  test("reports files chosen through the file input", async () => {
    const onFiles = vi.fn();
    render(<VideoUploader onFiles={onFiles} />);

    const input = screen.getByLabelText(fr.app.uploader.inputAria);
    await userEvent.upload(input, mp4("My Clip.mp4"));

    expect(onFiles).toHaveBeenCalledTimes(1);
    expect(onFiles.mock.calls[0][0][0]).toBeInstanceOf(File);
    expect(onFiles.mock.calls[0][0][0].name).toBe("My Clip.mp4");
  });

  test("reports files dropped onto the zone", () => {
    const onFiles = vi.fn();
    render(<VideoUploader onFiles={onFiles} />);

    const zone = screen.getByTestId("upload-zone");
    fireEvent.drop(zone, { dataTransfer: { files: [mp4()] } });

    expect(onFiles).toHaveBeenCalledTimes(1);
    expect(onFiles.mock.calls[0][0]).toHaveLength(1);
  });

  test("ignores a drop with no files", () => {
    const onFiles = vi.fn();
    render(<VideoUploader onFiles={onFiles} />);

    fireEvent.drop(screen.getByTestId("upload-zone"), {
      dataTransfer: { files: [] },
    });

    expect(onFiles).not.toHaveBeenCalled();
  });
});
