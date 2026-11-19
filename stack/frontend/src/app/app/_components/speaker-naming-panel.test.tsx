import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import type { SpeakerNaming } from "@/hooks/use-video-speakers";
import type { VideoSpeaker } from "@/lib/video/speakers";
import { SpeakerNamingPanel } from "./speaker-naming-panel";

const speaker = (overrides: Partial<VideoSpeaker> = {}): VideoSpeaker => ({
  label: "A",
  name: "Marine Durand",
  role: "principal",
  confidence: 0.92,
  method: "signals",
  evidence: [
    {
      kind: "handoff",
      quote: "Madame Durand, vous avez deux minutes.",
      at: 95,
    },
  ],
  operatorSet: false,
  ...overrides,
});

function naming(overrides: Partial<SpeakerNaming> = {}): SpeakerNaming {
  return {
    speakers: [speaker()],
    savingLabels: new Set(),
    rerunning: false,
    error: null,
    save: vi.fn(),
    rerun: vi.fn(),
    ...overrides,
  };
}

describe("SpeakerNamingPanel", () => {
  test("shows each voice with its name, role, confidence and evidence", () => {
    render(
      <SpeakerNamingPanel
        role="guest"
        naming={naming({
          speakers: [
            speaker(),
            speaker({ label: "B", name: "", role: "moderator", confidence: 0, method: "none", evidence: [] }),
            speaker({ label: "C", name: "Sophie Garnier", role: "guest", method: "operator", operatorSet: true, evidence: [] }),
          ],
        })}
      />,
    );

    const panel = screen.getByRole("region", {
      name: "Identification des intervenants",
    });
    const rows = within(panel).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("Voix A");
    expect(rows[0]).toHaveTextContent("Marine Durand");
    expect(rows[0]).toHaveTextContent("Intervenant principal");
    expect(rows[0]).toHaveTextContent("Confiance 92 %");
    expect(rows[0]).toHaveTextContent("Indices dans la transcription (1)");
    expect(rows[0]).toHaveTextContent("1:35");
    expect(rows[0]).toHaveTextContent("Passage de parole");
    expect(rows[0]).toHaveTextContent("Madame Durand, vous avez deux minutes.");
  });

  test("an unnamed voice says so and claims no confidence", () => {
    render(
      <SpeakerNamingPanel
        role="guest"
        naming={naming({
          speakers: [speaker({ name: "", confidence: 0, method: "none", evidence: [] })],
        })}
      />,
    );

    expect(screen.getByText("Non identifié")).toBeInTheDocument();
    expect(screen.queryByText(/Confiance/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Indices/)).not.toBeInTheDocument();
  });

  test("an operator-confirmed voice shows the confirmation instead of a confidence", () => {
    render(
      <SpeakerNamingPanel
        role="guest"
        naming={naming({ speakers: [speaker({ operatorSet: true, method: "operator", confidence: 1 })] })}
      />,
    );

    expect(screen.getByText("Confirmé par un opérateur")).toBeInTheDocument();
    expect(screen.queryByText(/Confiance/)).not.toBeInTheDocument();
  });

  test("a guest gets no controls, and no panel when there is nothing to read", () => {
    const { container, rerender } = render(
      <SpeakerNamingPanel role="guest" naming={naming()} />,
    );
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();

    rerender(<SpeakerNamingPanel role="guest" naming={naming({ speakers: [] })} />);
    expect(container).toBeEmptyDOMElement();
  });

  test("an admin renames a voice, sets its role and confirms it", () => {
    const save = vi.fn();
    render(<SpeakerNamingPanel role="admin" naming={naming({ save })} />);

    fireEvent.change(screen.getByRole("textbox", { name: "Nom de la voix A" }), {
      target: { value: "  Sophie Garnier " },
    });
    fireEvent.change(screen.getByRole("combobox", { name: "Rôle de la voix A" }), {
      target: { value: "guest" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirmer" }));

    expect(save).toHaveBeenCalledWith("A", {
      name: "Sophie Garnier",
      role: "guest",
    });
  });

  test("confirming without edits submits the proposal as it stands", () => {
    const save = vi.fn();
    render(<SpeakerNamingPanel role="admin" naming={naming({ save })} />);

    fireEvent.click(screen.getByRole("button", { name: "Confirmer" }));

    expect(save).toHaveBeenCalledWith("A", {
      name: "Marine Durand",
      role: "principal",
    });
  });

  test("the row being saved disables its button", () => {
    render(
      <SpeakerNamingPanel
        role="admin"
        naming={naming({
          speakers: [speaker(), speaker({ label: "B", name: "" })],
          savingLabels: new Set(["A"]),
        })}
      />,
    );

    expect(screen.getByRole("button", { name: "Enregistrement…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Confirmer" })).toBeEnabled();
  });

  test("an admin can re-run the step, also when no voice is named yet", () => {
    const rerun = vi.fn();
    const { rerender } = render(
      <SpeakerNamingPanel role="admin" naming={naming({ speakers: [], rerun })} />,
    );
    expect(
      screen.getByText("Aucun intervenant identifié pour cette analyse."),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Relancer l'identification" }),
    );
    expect(rerun).toHaveBeenCalledTimes(1);

    rerender(
      <SpeakerNamingPanel
        role="admin"
        naming={naming({ speakers: [], rerun, rerunning: true })}
      />,
    );
    expect(screen.getByRole("button", { name: "Identification…" })).toBeDisabled();
  });

  test("surfaces a failed save or re-run", () => {
    const { rerender } = render(
      <SpeakerNamingPanel role="admin" naming={naming({ error: "save" })} />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Le nom n'a pas pu être enregistré.",
    );

    rerender(
      <SpeakerNamingPanel role="admin" naming={naming({ error: "rerun" })} />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "L'identification a échoué ; les noms précédents sont conservés.",
    );
  });
});
