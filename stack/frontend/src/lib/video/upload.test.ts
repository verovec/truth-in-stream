import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { putWithProgress } from "./upload";
import type { PresignedRequest } from "./api";

type ProgressListener = (event: {
  lengthComputable: boolean;
  loaded: number;
  total: number;
}) => void;

// FakeXHR captures what the uploader does and lets each test drive the lifecycle
// (progress, load, error) deterministically, with no real network.
class FakeXHR {
  static instances: FakeXHR[] = [];
  method = "";
  url = "";
  headers: Record<string, string> = {};
  body: unknown = null;
  status = 0;
  aborted = false;
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  upload = {
    onprogress: null as ProgressListener | null,
  };

  constructor() {
    FakeXHR.instances.push(this);
  }

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }

  send(body: unknown) {
    this.body = body;
  }

  abort() {
    this.aborted = true;
    this.onabort?.();
  }
}

function makeFile() {
  return new File(["x".repeat(10)], "clip.mp4", { type: "video/mp4" });
}

const presigned: PresignedRequest = {
  url: "https://storage.example/uploads/vid-9.mp4?sig=put",
  method: "PUT",
  headers: {
    Host: ["storage.example"],
    "x-amz-meta-origin": ["browser"],
  },
};

beforeEach(() => {
  FakeXHR.instances = [];
  vi.stubGlobal("XMLHttpRequest", FakeXHR);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("putWithProgress", () => {
  test("PUTs the file to the presigned URL and reports progress", async () => {
    const onProgress = vi.fn();
    const promise = putWithProgress(presigned, makeFile(), onProgress);

    const xhr = FakeXHR.instances[0];
    expect(xhr.method).toBe("PUT");
    expect(xhr.url).toBe(presigned.url);
    expect(xhr.body).toBeInstanceOf(File);

    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 10 });
    expect(onProgress).toHaveBeenCalledWith(5, 10);

    xhr.status = 200;
    xhr.onload?.();
    await expect(promise).resolves.toBeUndefined();
  });

  test("replays signed headers but never the forbidden Host header", async () => {
    const promise = putWithProgress(presigned, makeFile(), () => {});
    const xhr = FakeXHR.instances[0];

    expect(xhr.headers["x-amz-meta-origin"]).toBe("browser");
    expect("Host" in xhr.headers).toBe(false);
    // Content-Type defaults to the file's type so it matches the signed request.
    expect(xhr.headers["Content-Type"]).toBe("video/mp4");

    xhr.status = 200;
    xhr.onload?.();
    await promise;
  });

  test("rejects with the status when storage rejects the upload", async () => {
    const promise = putWithProgress(presigned, makeFile(), () => {});
    const xhr = FakeXHR.instances[0];
    xhr.status = 403;
    xhr.onload?.();

    await expect(promise).rejects.toMatchObject({ status: 403 });
  });

  test("rejects on a network error", async () => {
    const promise = putWithProgress(presigned, makeFile(), () => {});
    const xhr = FakeXHR.instances[0];
    xhr.onerror?.();

    await expect(promise).rejects.toThrow(/network/i);
  });

  test("aborts the request when the signal fires", async () => {
    const controller = new AbortController();
    const promise = putWithProgress(
      presigned,
      makeFile(),
      () => {},
      controller.signal,
    );
    const xhr = FakeXHR.instances[0];

    controller.abort();
    expect(xhr.aborted).toBe(true);
    await expect(promise).rejects.toThrow(/abort/i);
  });
});
