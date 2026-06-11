// Direct-to-storage upload transport. The browser PUTs the file bytes straight
// to object storage using the presigned request the backend minted; the bytes
// never pass through our server.
//
// XMLHttpRequest is used rather than fetch because it is the only cross-browser
// way to observe upload progress: fetch's request-stream progress is unshipped
// in Safari and, where it exists, measures buffering rather than bytes on the
// wire (verified 2026-06). A presigned PUT is ~25 lines of XHR, so no upload
// library is warranted.
import { ApiError } from "@/lib/http";
import type { PresignedRequest } from "./api";

export type UploadProgress = (loaded: number, total: number) => void;

// PutUploader uploads a file via a presigned request, reporting progress. It is
// an injection seam: components depend on the type and tests pass a fake.
export type PutUploader = (
  presigned: PresignedRequest,
  file: File,
  onProgress: UploadProgress,
  signal?: AbortSignal,
) => Promise<void>;

// forbiddenHeaders are managed by the browser and cannot be set from script;
// the signed Host is already encoded in the URL, so replaying it is unnecessary.
const forbiddenHeaders = new Set(["host", "content-length", "connection"]);

export const putWithProgress: PutUploader = (
  presigned,
  file,
  onProgress,
  signal,
) =>
  new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error("upload aborted"));
      return;
    }
    const xhr = new XMLHttpRequest();
    xhr.open(presigned.method, presigned.url);

    let hasContentType = false;
    for (const [name, values] of Object.entries(presigned.headers)) {
      if (forbiddenHeaders.has(name.toLowerCase())) {
        continue;
      }
      if (name.toLowerCase() === "content-type") {
        hasContentType = true;
      }
      xhr.setRequestHeader(name, values.join(", "));
    }
    // The signed request expects the declared content type; default to the
    // file's so the PUT matches what the upload was requested with.
    if (!hasContentType && file.type) {
      xhr.setRequestHeader("Content-Type", file.type);
    }

    // Tear down the abort listener on every terminal path so a reused signal
    // does not accumulate listeners (and the XHR closures they capture).
    const onAbort = () => xhr.abort();
    const settle = (finish: () => void) => {
      signal?.removeEventListener("abort", onAbort);
      finish();
    };

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) {
        onProgress(event.loaded, event.total);
      }
    };
    xhr.onload = () =>
      settle(() => {
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve();
          return;
        }
        reject(
          new ApiError(`upload failed with status ${xhr.status}`, xhr.status),
        );
      });
    xhr.onerror = () =>
      settle(() => reject(new Error("upload failed: network error")));
    xhr.onabort = () => settle(() => reject(new Error("upload aborted")));

    signal?.addEventListener("abort", onAbort, { once: true });

    xhr.send(file);
  });
