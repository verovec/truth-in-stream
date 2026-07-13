import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { YoutubeEmbed, youtubeEmbedUrl } from "./youtube-embed";

describe("youtubeEmbedUrl", () => {
  test("embeds a channel's live stream from a canonical /channel/UC… reference", () => {
    expect(
      youtubeEmbedUrl("https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv"),
    ).toBe(
      "https://www.youtube.com/embed/live_stream?channel=UCabcdefghijklmnopqrstuv",
    );
  });

  test("embeds a specific video from a watch URL", () => {
    expect(youtubeEmbedUrl("https://www.youtube.com/watch?v=dQw4w9WgXcQ")).toBe(
      "https://www.youtube.com/embed/dQw4w9WgXcQ",
    );
  });

  test("embeds a youtu.be short link", () => {
    expect(youtubeEmbedUrl("https://youtu.be/dQw4w9WgXcQ")).toBe(
      "https://www.youtube.com/embed/dQw4w9WgXcQ",
    );
  });

  test("embeds a /live/<id> permalink", () => {
    expect(youtubeEmbedUrl("https://www.youtube.com/live/dQw4w9WgXcQ")).toBe(
      "https://www.youtube.com/embed/dQw4w9WgXcQ",
    );
  });

  test("returns null for a handle live URL (no derivable id)", () => {
    expect(youtubeEmbedUrl("https://www.youtube.com/@FRANCE24/live")).toBeNull();
  });

  test("returns null for a legacy /c/ custom URL", () => {
    expect(youtubeEmbedUrl("https://www.youtube.com/c/euronewsfr/live")).toBeNull();
  });

  test("returns null for a non-YouTube host", () => {
    expect(youtubeEmbedUrl("https://vimeo.com/channels/live")).toBeNull();
  });

  test("returns null for a non-http(s) or malformed reference", () => {
    expect(youtubeEmbedUrl("javascript:alert(1)")).toBeNull();
    expect(youtubeEmbedUrl("not a url")).toBeNull();
  });
});

describe("YoutubeEmbed", () => {
  test("renders an iframe when an embed URL can be derived", () => {
    render(
      <YoutubeEmbed
        sourceRef="https://www.youtube.com/watch?v=dQw4w9WgXcQ"
        title="France 24"
      />,
    );
    const iframe = screen.getByTitle("France 24");
    expect(iframe.tagName).toBe("IFRAME");
    expect(iframe).toHaveAttribute(
      "src",
      "https://www.youtube.com/embed/dQw4w9WgXcQ",
    );
    expect(iframe).toHaveAttribute("loading", "lazy");
    expect(iframe).toHaveAttribute("allowfullscreen");
  });

  test("falls back to an open-on-YouTube link when no embed URL can be formed", () => {
    render(
      <YoutubeEmbed
        sourceRef="https://www.youtube.com/@FRANCE24/live"
        title="France 24"
      />,
    );
    expect(screen.queryByTitle("France 24")).not.toBeInTheDocument();
    const link = screen.getByRole("link", {
      name: fr.app.tv.embed.openOnYoutube,
    });
    expect(link).toHaveAttribute("href", "https://www.youtube.com/@FRANCE24/live");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  test("renders no link for a non-http(s) source_ref (no javascript: XSS vector)", () => {
    render(
      // A poisoned/mistaken registry entry: the fallback must never render this
      // as a clickable href.
      <YoutubeEmbed
        sourceRef="javascript:alert(document.cookie)"
        title="Evil"
      />,
    );
    expect(
      screen.getByText(fr.app.tv.embed.unavailable),
    ).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
