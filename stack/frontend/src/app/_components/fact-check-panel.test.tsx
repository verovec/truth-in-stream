import { act, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { renderWithPlayback } from "@/test/playback";
import { FactCheckPanel } from "./fact-check-panel";

describe("FactCheckPanel", () => {
  test("renders as a complementary landmark with a heading", () => {
    renderWithPlayback(<FactCheckPanel />);

    expect(
      screen.getByRole("complementary", { name: /fact checks/i }),
    ).toBeInTheDocument();
  });

  test("shows the playback position as it advances", () => {
    const { store } = renderWithPlayback(<FactCheckPanel />);

    expect(screen.getByText("0:00")).toBeInTheDocument();

    act(() => store.update({ currentTime: 754 }));

    expect(screen.getByText("12:34")).toBeInTheDocument();
  });
});
