import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { HtmlLang } from "./html-lang";

describe("HtmlLang", () => {
  test("sets the document language to the active locale", () => {
    document.documentElement.lang = "fr";
    render(<HtmlLang locale="en" />);
    expect(document.documentElement.lang).toBe("en");
  });
});
