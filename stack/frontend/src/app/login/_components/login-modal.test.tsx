import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { back } from "@/test/next-navigation-mock";
import { LoginModal } from "./login-modal";

vi.mock("next/navigation", () => import("@/test/next-navigation-mock"));

beforeEach(() => {
  back.mockClear();
});

describe("LoginModal", () => {
  test("renders a labelled modal dialog around its children", () => {
    render(
      <LoginModal>
        <button type="button">inner control</button>
      </LoginModal>,
    );

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAccessibleName("Sign in");
    expect(
      screen.getByRole("button", { name: /inner control/i }),
    ).toBeInTheDocument();
  });

  test("moves focus to the first form field on open", () => {
    render(
      <LoginModal>
        <input aria-label="email" />
        <input aria-label="password" />
      </LoginModal>,
    );

    expect(screen.getByLabelText("email")).toHaveFocus();
  });

  test("falls back to focusing the dialog when there is no field", () => {
    render(
      <LoginModal>
        <button type="button">inner control</button>
      </LoginModal>,
    );

    expect(screen.getByRole("dialog")).toHaveFocus();
  });

  test("closes on Escape", () => {
    render(
      <LoginModal>
        <button type="button">inner control</button>
      </LoginModal>,
    );

    fireEvent.keyDown(document, { key: "Escape" });

    expect(back).toHaveBeenCalledTimes(1);
  });

  test("closes when the backdrop is clicked", () => {
    render(
      <LoginModal>
        <button type="button">inner control</button>
      </LoginModal>,
    );

    const backdrop = screen.getByRole("dialog").parentElement as HTMLElement;
    fireEvent.mouseDown(backdrop);

    expect(back).toHaveBeenCalledTimes(1);
  });

  test("does not close when the dialog body is clicked", () => {
    render(
      <LoginModal>
        <button type="button">inner control</button>
      </LoginModal>,
    );

    fireEvent.mouseDown(screen.getByRole("dialog"));

    expect(back).not.toHaveBeenCalled();
  });

  test("closes from the close button", async () => {
    const user = userEvent.setup();
    render(
      <LoginModal>
        <button type="button">inner control</button>
      </LoginModal>,
    );

    await user.click(screen.getByRole("button", { name: /close/i }));

    expect(back).toHaveBeenCalledTimes(1);
  });
});
