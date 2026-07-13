import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import { ApiError } from "@/lib/http";
import type { Channel } from "@/lib/tv/api";
import { BackofficeTvChannelForm } from "./backoffice-tv-channel-form";

const copy = fr.app.backoffice.tv.form;

function channel(over: Partial<Channel> = {}): Channel {
  return {
    id: "chan-1",
    slug: "france-24",
    name: "France 24",
    sourceKind: "youtube",
    sourceRef: "https://www.youtube.com/@FRANCE24/live",
    enabled: true,
    archiveEnabled: false,
    live: true,
    ...over,
  };
}

function fill(values: { slug?: string; name?: string; sourceRef?: string }) {
  if (values.slug !== undefined) {
    fireEvent.change(screen.getByLabelText(copy.slug), {
      target: { value: values.slug },
    });
  }
  if (values.name !== undefined) {
    fireEvent.change(screen.getByLabelText(copy.name), {
      target: { value: values.name },
    });
  }
  if (values.sourceRef !== undefined) {
    fireEvent.change(screen.getByLabelText(copy.sourceRef), {
      target: { value: values.sourceRef },
    });
  }
}

afterEach(() => vi.restoreAllMocks());

describe("BackofficeTvChannelForm add mode", () => {
  test("rejects an invalid slug and never calls create", () => {
    const create = vi.fn();
    render(
      <BackofficeTvChannelForm
        editing={null}
        create={create}
        update={vi.fn()}
        onSaved={vi.fn()}
        onCancelEdit={vi.fn()}
      />,
    );
    fill({ slug: "Bad Slug", name: "France 24", sourceRef: "https://x/live" });
    fireEvent.click(screen.getByRole("button", { name: copy.submitAdd }));

    expect(create).not.toHaveBeenCalled();
    expect(screen.getByText(copy.errors.slug)).toBeInTheDocument();
  });

  test("rejects an empty name and empty source reference", () => {
    const create = vi.fn();
    render(
      <BackofficeTvChannelForm
        editing={null}
        create={create}
        update={vi.fn()}
        onSaved={vi.fn()}
        onCancelEdit={vi.fn()}
      />,
    );
    fill({ slug: "france-24", name: "  ", sourceRef: "  " });
    fireEvent.click(screen.getByRole("button", { name: copy.submitAdd }));

    expect(create).not.toHaveBeenCalled();
    expect(screen.getByText(copy.errors.name)).toBeInTheDocument();
    expect(screen.getByText(copy.errors.sourceRef)).toBeInTheDocument();
  });

  test("creates a channel with trimmed, valid input and its toggle defaults", async () => {
    const create = vi.fn().mockResolvedValue(channel());
    const onSaved = vi.fn();
    render(
      <BackofficeTvChannelForm
        editing={null}
        create={create}
        update={vi.fn()}
        onSaved={onSaved}
        onCancelEdit={vi.fn()}
      />,
    );
    fill({
      slug: "  france-24  ",
      name: "  France 24  ",
      sourceRef: "  https://www.youtube.com/@FRANCE24/live  ",
    });
    fireEvent.click(screen.getByRole("button", { name: copy.submitAdd }));

    await waitFor(() =>
      expect(create).toHaveBeenCalledWith({
        slug: "france-24",
        name: "France 24",
        sourceKind: "youtube",
        sourceRef: "https://www.youtube.com/@FRANCE24/live",
        enabled: true,
        archiveEnabled: false,
      }),
    );
    expect(onSaved).toHaveBeenCalledTimes(1);
  });

  test("submits the chosen HLS source kind", async () => {
    const create = vi.fn().mockResolvedValue(channel({ sourceKind: "hls" }));
    render(
      <BackofficeTvChannelForm
        editing={null}
        create={create}
        update={vi.fn()}
        onSaved={vi.fn()}
        onCancelEdit={vi.fn()}
      />,
    );
    fill({
      slug: "public-senat",
      name: "Public Sénat",
      sourceRef: "https://stream.example/senat.m3u8",
    });
    fireEvent.change(screen.getByLabelText(copy.sourceKind), {
      target: { value: "hls" },
    });
    fireEvent.click(screen.getByRole("button", { name: copy.submitAdd }));

    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({ sourceKind: "hls" }),
      ),
    );
  });

  test("surfaces a 409 duplicate-slug error inline", async () => {
    const create = vi.fn().mockRejectedValue(new ApiError("slug already exists", 409));
    render(
      <BackofficeTvChannelForm
        editing={null}
        create={create}
        update={vi.fn()}
        onSaved={vi.fn()}
        onCancelEdit={vi.fn()}
      />,
    );
    fill({ slug: "france-24", name: "France 24", sourceRef: "https://x/live" });
    fireEvent.click(screen.getByRole("button", { name: copy.submitAdd }));

    expect(
      await screen.findByText(
        formatTemplate(copy.saveError, { message: "slug already exists" }),
      ),
    ).toBeInTheDocument();
  });

  test("surfaces a 400 validation error inline", async () => {
    const create = vi.fn().mockRejectedValue(new ApiError("youtube or hls", 400));
    render(
      <BackofficeTvChannelForm
        editing={null}
        create={create}
        update={vi.fn()}
        onSaved={vi.fn()}
        onCancelEdit={vi.fn()}
      />,
    );
    fill({ slug: "france-24", name: "France 24", sourceRef: "https://x/live" });
    fireEvent.click(screen.getByRole("button", { name: copy.submitAdd }));

    expect(
      await screen.findByText(
        formatTemplate(copy.saveError, { message: "youtube or hls" }),
      ),
    ).toBeInTheDocument();
  });
});

describe("BackofficeTvChannelForm edit mode", () => {
  test("prefills, keeps the slug read-only, and patches via update", async () => {
    const update = vi.fn().mockResolvedValue(channel({ name: "France 24 HD" }));
    const onSaved = vi.fn();
    render(
      <BackofficeTvChannelForm
        editing={channel()}
        create={vi.fn()}
        update={update}
        onSaved={onSaved}
        onCancelEdit={vi.fn()}
      />,
    );

    const slugInput = screen.getByLabelText(copy.slug) as HTMLInputElement;
    expect(slugInput.value).toBe("france-24");
    expect(slugInput).toBeDisabled();

    fireEvent.change(screen.getByLabelText(copy.name), {
      target: { value: "France 24 HD" },
    });
    fireEvent.click(screen.getByRole("button", { name: copy.submitEdit }));

    await waitFor(() =>
      expect(update).toHaveBeenCalledWith("chan-1", {
        name: "France 24 HD",
        sourceKind: "youtube",
        sourceRef: "https://www.youtube.com/@FRANCE24/live",
        enabled: true,
        archiveEnabled: false,
      }),
    );
    expect(onSaved).toHaveBeenCalledTimes(1);
  });
});
