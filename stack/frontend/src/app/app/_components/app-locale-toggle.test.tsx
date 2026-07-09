import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { AppLocaleToggle } from "./app-locale-toggle";

describe("AppLocaleToggle", () => {
  test("marks the active locale and exposes both options", () => {
    render(
      <AppLocaleToggle
        activeLocale="fr"
        copy={fr.langSwitch}
        action={vi.fn()}
      />,
    );

    const active = screen.getByRole("button", { name: fr.langSwitch.toFrench });
    expect(active).toHaveAttribute("aria-pressed", "true");
    expect(active).toHaveTextContent("FR");
    const other = screen.getByRole("button", { name: fr.langSwitch.toEnglish });
    expect(other).toHaveAttribute("aria-pressed", "false");
    expect(other).toHaveTextContent("EN");
  });

  test("switching locales invokes the preference action once", async () => {
    const user = userEvent.setup();
    const action = vi.fn().mockResolvedValue(undefined);
    render(
      <AppLocaleToggle activeLocale="fr" copy={fr.langSwitch} action={action} />,
    );

    await user.click(screen.getByRole("button", { name: fr.langSwitch.toEnglish }));

    await waitFor(() => {
      expect(action).toHaveBeenCalledExactlyOnceWith("en");
    });
  });

  test("re-selecting the active locale does nothing", async () => {
    const user = userEvent.setup();
    const action = vi.fn();
    render(
      <AppLocaleToggle activeLocale="fr" copy={fr.langSwitch} action={action} />,
    );

    await user.click(screen.getByRole("button", { name: fr.langSwitch.toFrench }));

    expect(action).not.toHaveBeenCalled();
  });
});
