// Client for the admin-only analysis exports (stack/backend
// internal/handler/export.go). The endpoints stream a completed video's cached
// snapshot as an SRT transcript or a CSV decision trace; both ride the same-origin
// proxy so the backend session cookie authenticates them exactly like the rest of
// the authenticated client. A missing snapshot is a 404 the caller surfaces inline
// rather than failing silently.
import { API_BASE, toApiError } from "@/lib/http";

export type ExportFormat = "srt" | "csv";

type ExportSpec = {
  path: string;
  filename: string;
};

function exportSpec(videoId: string, format: ExportFormat): ExportSpec {
  if (format === "srt") {
    return {
      path: `${API_BASE}/api/videos/${videoId}/export/transcript.srt`,
      filename: `${videoId}.srt`,
    };
  }
  return {
    path: `${API_BASE}/api/videos/${videoId}/export/claims.csv`,
    filename: `${videoId}.csv`,
  };
}

// FetchedExport is a downloaded export's bytes and the filename the server named
// it (from Content-Disposition), falling back to a videoId-based name when the
// header is absent.
export type FetchedExport = {
  blob: Blob;
  filename: string;
};

// fetchExport requests one export and returns its bytes plus the server-named
// filename. It throws an ApiError (carrying the 404 status when no snapshot is
// cached) so the caller can branch on the missing-snapshot case. The credentials
// option forwards the session cookie on the same-origin proxy.
export async function fetchExport(
  videoId: string,
  format: ExportFormat,
  signal?: AbortSignal,
): Promise<FetchedExport> {
  const { path, filename } = exportSpec(videoId, format);
  const response = await fetch(path, { credentials: "same-origin", signal });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const blob = await response.blob();
  return {
    blob,
    filename:
      filenameFromDisposition(response.headers.get("content-disposition")) ??
      filename,
  };
}

// filenameFromDisposition extracts the filename from a Content-Disposition header
// value, or null when the header is absent or carries no filename.
export function filenameFromDisposition(value: string | null): string | null {
  if (!value) {
    return null;
  }
  const match = /filename="?([^"]+)"?/i.exec(value);
  return match ? match[1] : null;
}

// triggerDownload saves a blob to disk under filename using a transient object URL
// and a synthetic anchor click - the only portable way to name a fetched download.
export function triggerDownload(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

// downloadExport fetches and saves an export, returning the filename it used.
export async function downloadExport(
  videoId: string,
  format: ExportFormat,
  signal?: AbortSignal,
): Promise<string> {
  const { blob, filename } = await fetchExport(videoId, format, signal);
  triggerDownload(blob, filename);
  return filename;
}
