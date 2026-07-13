// Typed client for the TV channel registry (stack/backend internal/handler
// tv channels). A channel is addressed by its durable UUID id; the kebab slug is
// its stable public handle. The list is readable by any authenticated user; the
// create/update/delete mutations are admin-gated on the backend, so the
// backoffice section is the single surface that turns a capture feed on or off.
import { API_BASE, toApiError } from "@/lib/http";

export type SourceKind = "youtube" | "hls";

// SOURCE_KINDS is the closed set the backend accepts; the form validates against
// it before a create/update leaves the browser so a bad kind fails fast.
export const SOURCE_KINDS = ["youtube", "hls"] as const;

export function isSourceKind(value: string): value is SourceKind {
  return (SOURCE_KINDS as readonly string[]).includes(value);
}

// SLUG_PATTERN mirrors the backend's kebab-case rule for a channel slug: lower
// alphanumerics in dash-separated groups, no leading/trailing/double dash.
export const SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

// Channel is one row of the registry. `live` reflects whether a capture feed is
// currently connected; `enabled` gates capture and `archiveEnabled` gates
// persisting the recording. There is no "last recording" field: the backend
// exposes none, and the recordings listing belongs to the /tv page.
export type Channel = {
  id: string;
  slug: string;
  name: string;
  sourceKind: SourceKind;
  sourceRef: string;
  enabled: boolean;
  archiveEnabled: boolean;
  live: boolean;
};

// ChannelCreateInput is the create form's payload. enabled and archiveEnabled
// are optional so the backend can apply its own defaults when omitted.
export type ChannelCreateInput = {
  slug: string;
  name: string;
  sourceKind: SourceKind;
  sourceRef: string;
  enabled?: boolean;
  archiveEnabled?: boolean;
};

// ChannelUpdateInput is any subset of the mutable fields. The slug is immutable
// after creation, so it is absent here.
export type ChannelUpdateInput = Partial<{
  name: string;
  sourceKind: SourceKind;
  sourceRef: string;
  enabled: boolean;
  archiveEnabled: boolean;
}>;

type ChannelWire = {
  id: string;
  slug: string;
  name: string;
  source_kind: SourceKind;
  source_ref: string;
  enabled: boolean;
  archive_enabled: boolean;
  live: boolean;
};

type ListWire = { channels?: ChannelWire[] };

function normalizeChannel(wire: ChannelWire): Channel {
  return {
    id: wire.id,
    slug: wire.slug,
    name: wire.name,
    sourceKind: wire.source_kind,
    sourceRef: wire.source_ref,
    enabled: wire.enabled,
    archiveEnabled: wire.archive_enabled,
    live: wire.live,
  };
}

// toUpdateBody translates the camelCase patch to the backend's snake_case body,
// carrying only the keys the caller supplied so a subset PATCH stays a subset.
function toUpdateBody(patch: ChannelUpdateInput): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  if (patch.name !== undefined) {
    body.name = patch.name;
  }
  if (patch.sourceKind !== undefined) {
    body.source_kind = patch.sourceKind;
  }
  if (patch.sourceRef !== undefined) {
    body.source_ref = patch.sourceRef;
  }
  if (patch.enabled !== undefined) {
    body.enabled = patch.enabled;
  }
  if (patch.archiveEnabled !== undefined) {
    body.archive_enabled = patch.archiveEnabled;
  }
  return body;
}

export async function listChannels(signal?: AbortSignal): Promise<Channel[]> {
  const response = await fetch(`${API_BASE}/api/tv/channels`, { signal });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ListWire;
  return (wire.channels ?? []).map(normalizeChannel);
}

// createChannel registers a new channel. The backend answers 201 with the
// record; a bad slug/name/source is a 400 and a duplicate slug a 409, both
// carrying a message the caller surfaces inline.
export async function createChannel(
  input: ChannelCreateInput,
  signal?: AbortSignal,
): Promise<Channel> {
  const body: Record<string, unknown> = {
    slug: input.slug,
    name: input.name,
    source_kind: input.sourceKind,
    source_ref: input.sourceRef,
  };
  if (input.enabled !== undefined) {
    body.enabled = input.enabled;
  }
  if (input.archiveEnabled !== undefined) {
    body.archive_enabled = input.archiveEnabled;
  }
  const response = await fetch(`${API_BASE}/api/tv/channels`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal,
  });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ChannelWire;
  return normalizeChannel(wire);
}

// updateChannel patches a subset of a channel's mutable fields (used both by the
// edit form and by the enabled/archive toggles). It answers 200 with the updated
// record, 404 when the id is unknown.
export async function updateChannel(
  id: string,
  patch: ChannelUpdateInput,
  signal?: AbortSignal,
): Promise<Channel> {
  const response = await fetch(
    `${API_BASE}/api/tv/channels/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(toUpdateBody(patch)),
      signal,
    },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ChannelWire;
  return normalizeChannel(wire);
}

// deleteChannel removes a channel. It answers 204 with no body on success, 404
// when the id is unknown; nothing is parsed on success, so callers await void.
export async function deleteChannel(
  id: string,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch(
    `${API_BASE}/api/tv/channels/${encodeURIComponent(id)}`,
    { method: "DELETE", signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
}
