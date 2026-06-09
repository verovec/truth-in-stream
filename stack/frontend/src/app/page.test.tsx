import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import Home from "./page";

test("home page renders content", () => {
  const { container } = render(<Home />);
  expect(container.firstChild).not.toBeNull();
});
