import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { RecapDocumentProps } from "@/lib/recap-pdf/recap-document";
import { debateRecap } from "@/test/recap";
import { ExportPdfButton, type PdfAvailability } from "./export-pdf-button";

const t = fr.app.recap.pdf;

function ready(): Extract<PdfAvailability, { status: "ready" }> {
  const recap = debateRecap();
  return { status: "ready", recap, sheets: [recap.speakers[1], recap.speakers[0]] };
}

describe("ExportPdfButton", () => {
  test("one press exports the recap on screen, in the order the page shows", async () => {
    const exportPdf = vi.fn(async (props: RecapDocumentProps) => {
      void props;
    });
    const availability = ready();
    render(
      <ExportPdfButton
        availability={availability}
        brandName="jeminforme.fr"
        exportPdf={exportPdf}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: t.export }));

    expect(exportPdf).toHaveBeenCalledTimes(1);
    const props = exportPdf.mock.calls[0][0];
    expect(props.recap).toBe(availability.recap);
    expect(props.sheets.map((speaker) => speaker.label)).toEqual(["B", "A"]);
    expect(props.brandName).toBe("jeminforme.fr");
    expect(props.locale).toBe("fr");
    expect(props.t).toBe(fr.app);
  });

  test("shows that it is working and cannot be pressed twice meanwhile", async () => {
    let finish: () => void = () => {};
    const exportPdf = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    render(
      <ExportPdfButton availability={ready()} brandName="b" exportPdf={exportPdf} />,
    );

    await userEvent.click(screen.getByRole("button", { name: t.export }));
    const busy = screen.getByRole("button", { name: t.exporting });
    expect(busy).toBeDisabled();
    await userEvent.click(busy);
    expect(exportPdf).toHaveBeenCalledTimes(1);

    finish();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: t.export })).toBeEnabled(),
    );
  });

  test("a failed export says so and can be retried", async () => {
    const exportPdf = vi
      .fn<(props: RecapDocumentProps) => Promise<void>>()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce();
    render(
      <ExportPdfButton availability={ready()} brandName="b" exportPdf={exportPdf} />,
    );

    await userEvent.click(screen.getByRole("button", { name: t.export }));
    expect(await screen.findByRole("alert")).toHaveTextContent(t.failed);

    await userEvent.click(screen.getByRole("button", { name: t.export }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(exportPdf).toHaveBeenCalledTimes(2);
  });

  test.each([
    ["loading", t.unavailableLoading],
    ["notReady", t.unavailableNotReady],
    ["failed", t.unavailable],
  ] as const)("is unavailable with a reason while %s", async (reason, message) => {
    const exportPdf = vi.fn(async () => {});
    render(
      <ExportPdfButton
        availability={{ status: "unavailable", reason }}
        brandName="b"
        exportPdf={exportPdf}
      />,
    );

    const button = screen.getByRole("button", { name: t.export });
    expect(button).toBeDisabled();
    // The reason is visible and is the button's accessible description.
    expect(button).toHaveAccessibleDescription(message);
    await userEvent.click(button);
    expect(exportPdf).not.toHaveBeenCalled();
  });
});
