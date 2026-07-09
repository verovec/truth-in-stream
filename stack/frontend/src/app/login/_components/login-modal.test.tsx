import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { back } from "@/test/next-navigation-mock";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { LoginModal } from "./login-modal";

vi.mock("next/navigation", () => import("@/test/next-navigation-mock"));

const copy = {
  title: fr.login.modalTitle,
  intro: fr.login.modalIntro,
  close: fr.login.close,
};

function renderModal() {
  return render(
    <LoginModal locale="fr" copy={copy}>
      <button type="button">inner control</button>
    </LoginModal>,
  );
}

beforeEach(() => {
  back.mockClear();
});

describe("LoginModal", () => {
  test("renders a labelled modal dialog around its children", () => {
    renderModal();

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAccessibleName(fr.login.modalTitle);
    expect(dialog).toHaveAccessibleDescription(fr.login.modalIntro);
    expect(
      screen.getByRole("button", { name: /inner control/i }),
    ).toBeInTheDocument();
  });

  test("labels the dialog subtree with its locale for assistive tech", () => {
    // The modal renders in the root layout's {auth} slot under <html lang="en">,
    // so it must carry its own lang or a screen reader mispronounces the
    // localized copy (WCAG 3.1.2).
    renderModal();

    expect(screen.getByRole("dialog")).toHaveAttribute("lang", "fr");
  });

  test("moves focus into the dialog on open", () => {
    renderModal();

    expect(screen.getByRole("dialog")).toHaveFocus();
  });

  test("closes on Escape", () => {
    renderModal();

    fireEvent.keyDown(document, { key: "Escape" });

    expect(back).toHaveBeenCalledTimes(1);
  });

  test("closes when the backdrop is clicked", () => {
    renderModal();

    const backdrop = screen.getByRole("dialog").parentElement as HTMLElement;
    fireEvent.mouseDown(backdrop);

    expect(back).toHaveBeenCalledTimes(1);
  });

  test("does not close when the dialog body is clicked", () => {
    renderModal();

    fireEvent.mouseDown(screen.getByRole("dialog"));

    expect(back).not.toHaveBeenCalled();
  });

  test("closes from the close button", async () => {
    const user = userEvent.setup();
    renderModal();

    await user.click(screen.getByRole("button", { name: fr.login.close }));

    expect(back).toHaveBeenCalledTimes(1);
  });
});
