import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import type { PutUploader } from "@/lib/video/upload";
import { useVideoUploads } from "./use-video-uploads";

function mp4(name = "clip.mp4") {
  return new File(["x".repeat(20)], name, { type: "video/mp4" });
}

function uploadRoutes(ticketStatus = 201, confirmStatus = 200) {
  return [
    {
      match: (u: string, i?: RequestInit) =>
        u.endsWith("/api/videos/uploads") && i?.method === "POST",
      responses: [
        json(ticketStatus, {
          video_id: "vid-9",
          object_key: "uploads/vid-9.mp4",
          status: "pending",
          upload: { url: "https://storage/put", method: "PUT", headers: {} },
        }),
      ],
    },
    {
      match: (u: string) => u.endsWith("/confirm"),
      responses: [
        json(confirmStatus, {
          id: "vid-9",
          title: "clip",
          status: "ready",
          kind: "upload",
          content_type: "video/mp4",
          size_bytes: 20,
          created_at: "2026-06-11T10:00:00Z",
          updated_at: "2026-06-11T10:00:00Z",
        }),
      ],
    },
  ];
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("useVideoUploads", () => {
  test("drives a file from request through progress to a ready record", async () => {
    stubBackend(uploadRoutes());
    const onUploaded = vi.fn();
    const uploader: PutUploader = async (_p, _f, onProgress) => {
      onProgress(5, 10);
    };

    const { result } = renderHook(() =>
      useVideoUploads({ uploader, onUploaded }),
    );

    act(() => {
      result.current.startUploads([mp4("My Clip.mp4")]);
    });

    expect(result.current.jobs).toHaveLength(1);
    expect(result.current.jobs[0].title).toBe("My Clip");

    await waitFor(() => {
      expect(result.current.jobs[0].state.status).toBe("ready");
    });
    expect(onUploaded).toHaveBeenCalledWith(
      expect.objectContaining({ id: "vid-9", status: "ready" }),
    );
  });

  test("reports progress as a fraction while uploading", async () => {
    stubBackend(uploadRoutes());
    let release: () => void = () => {};
    const uploader: PutUploader = (_p, _f, onProgress) =>
      new Promise((resolve) => {
        onProgress(3, 12);
        release = resolve;
      });

    const { result } = renderHook(() => useVideoUploads({ uploader }));
    act(() => {
      result.current.startUploads([mp4()]);
    });

    await waitFor(() => {
      expect(result.current.jobs[0].state).toEqual({
        status: "uploading",
        progress: 0.25,
      });
    });

    act(() => {
      release();
    });
    await waitFor(() => {
      expect(result.current.jobs[0].state.status).toBe("ready");
    });
  });

  test("rejects an unsupported content type before any request", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const { result } = renderHook(() => useVideoUploads({}));

    act(() => {
      result.current.startUploads([
        new File(["x"], "notes.txt", { type: "text/plain" }),
      ]);
    });

    expect(result.current.jobs[0].state.status).toBe("error");
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  test("surfaces a transport failure as an error job", async () => {
    stubBackend(uploadRoutes());
    const uploader: PutUploader = () =>
      Promise.reject(new Error("upload failed: network error"));

    const { result } = renderHook(() => useVideoUploads({ uploader }));
    act(() => {
      result.current.startUploads([mp4()]);
    });

    await waitFor(() => {
      expect(result.current.jobs[0].state.status).toBe("error");
    });
  });

  test("surfaces a rejected upload request as an error job", async () => {
    stubBackend([
      {
        match: (u: string, i?: RequestInit) =>
          u.endsWith("/api/videos/uploads") && i?.method === "POST",
        responses: [json(413, { error: "declared size is out of range" })],
      },
    ]);
    const uploader: PutUploader = async () => {};

    const { result } = renderHook(() => useVideoUploads({ uploader }));
    act(() => {
      result.current.startUploads([mp4()]);
    });

    await waitFor(() => {
      const { state } = result.current.jobs[0];
      expect(state.status).toBe("error");
      if (state.status === "error") {
        expect(state.message).toMatch(/out of range/);
      }
    });
  });
});
