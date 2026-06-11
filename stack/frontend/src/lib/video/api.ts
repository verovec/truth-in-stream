// Typed client for the video library and upload API (stack/backend
// internal/handler/videos.go). A video has a durable UUID identity here, distinct
// from the batch processing identity in the fact-check client. The object key is
// never exposed: clients address a video by id and upload/play through presigned
// requests the backend mints.
import { toApiError } from "@/lib/http";

const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "";

export type VideoStatus = "pending" | "ready" | "failed";
export type VideoKind = "upload" | "sample";

// ACCEPTED_VIDEO_TYPES mirrors the backend's allowed upload content types
// (stack/backend internal/service/video.go). The browser validates against it
// before requesting an upload so an unsupported file fails fast and locally.
export const ACCEPTED_VIDEO_TYPES = [
  "video/mp4",
  "video/webm",
  "video/ogg",
  "video/quicktime",
] as const;

export function isAcceptedVideoType(type: string): boolean {
  return (ACCEPTED_VIDEO_TYPES as readonly string[]).includes(type);
}

// LibraryVideo is one row of the library: a curated sample or an operator upload.
export type LibraryVideo = {
  id: string;
  title: string;
  status: VideoStatus;
  kind: VideoKind;
  contentType: string;
  sizeBytes: number;
  createdAt: string;
  updatedAt: string;
};

// PresignedRequest is a pre-authorized request the browser issues directly to
// object storage: it sends Method to URL replaying every header in Headers.
export type PresignedRequest = {
  url: string;
  method: string;
  headers: Record<string, string[]>;
};

// UploadTicket is a pending video record plus the presigned PUT the browser uses
// to upload the file bytes directly to storage.
export type UploadTicket = {
  videoId: string;
  objectKey: string;
  status: VideoStatus;
  upload: PresignedRequest;
};

// PlayableVideo is a video record plus a presigned, range-capable playback URL.
export type PlayableVideo = LibraryVideo & {
  playback: PresignedRequest;
};

export type UploadRequestInput = {
  title: string;
  contentType: string;
  sizeBytes: number;
};

type VideoWire = {
  id: string;
  title: string;
  status: VideoStatus;
  kind: VideoKind;
  content_type: string;
  size_bytes: number;
  created_at: string;
  updated_at: string;
};

type PresignedWire = {
  url: string;
  method: string;
  headers: Record<string, string[]>;
};

type ListWire = { videos?: VideoWire[] };
type PlayableWire = VideoWire & { playback: PresignedWire };
type UploadTicketWire = {
  video_id: string;
  object_key: string;
  status: VideoStatus;
  upload: PresignedWire;
};

function normalizeVideo(wire: VideoWire): LibraryVideo {
  return {
    id: wire.id,
    title: wire.title,
    status: wire.status,
    kind: wire.kind,
    contentType: wire.content_type,
    sizeBytes: wire.size_bytes,
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
  };
}

function normalizePresigned(wire: PresignedWire): PresignedRequest {
  return { url: wire.url, method: wire.method, headers: wire.headers ?? {} };
}

export async function listVideos(signal?: AbortSignal): Promise<LibraryVideo[]> {
  const response = await fetch(`${API_BASE}/api/videos`, { signal });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ListWire;
  return (wire.videos ?? []).map(normalizeVideo);
}

export async function getVideo(
  id: string,
  signal?: AbortSignal,
): Promise<PlayableVideo> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(id)}`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as PlayableWire;
  return { ...normalizeVideo(wire), playback: normalizePresigned(wire.playback) };
}

export async function requestUpload(
  input: UploadRequestInput,
  signal?: AbortSignal,
): Promise<UploadTicket> {
  const response = await fetch(`${API_BASE}/api/videos/uploads`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      title: input.title,
      content_type: input.contentType,
      size_bytes: input.sizeBytes,
    }),
    signal,
  });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as UploadTicketWire;
  return {
    videoId: wire.video_id,
    objectKey: wire.object_key,
    status: wire.status,
    upload: normalizePresigned(wire.upload),
  };
}

export async function confirmVideo(
  id: string,
  signal?: AbortSignal,
): Promise<LibraryVideo> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(id)}/confirm`,
    { method: "POST", signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as VideoWire;
  return normalizeVideo(wire);
}
