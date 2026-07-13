"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";

// YOUTUBE_HOSTS is the closed set of hosts a channel's source_ref may name for
// its embed to be trusted; anything else yields no embed (the fallback link is
// shown instead), so a malformed or hostile source_ref never loads a foreign
// iframe.
const YOUTUBE_HOSTS = new Set([
  "youtube.com",
  "m.youtube.com",
  "youtube-nocookie.com",
  "youtu.be",
]);

// VIDEO_ID matches YouTube's 11-char video id vocabulary so a stray path segment
// is not mistaken for one.
const VIDEO_ID = /^[\w-]{11}$/;

// CHANNEL_ID matches a canonical UC… channel id, the only channel form that a
// live-stream embed can be built from without an API lookup.
const CHANNEL_ID = /^UC[\w-]{22}$/;

/**
 * Derives an official YouTube iframe embed URL from a channel's source_ref, or
 * null when one cannot be formed reliably. A canonical /channel/UC… reference
 * embeds that channel's current live stream; a watch/short/embed/live/youtu.be
 * reference embeds that specific video. A handle (@name), legacy username, or
 * /c/ custom URL carries no id an embed can be built from client-side, so it
 * returns null and the caller shows an "open on YouTube" link instead of a
 * broken frame. Exported for direct unit testing.
 */
export function youtubeEmbedUrl(sourceRef: string): string | null {
  let url: URL;
  try {
    url = new URL(sourceRef);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    return null;
  }
  const host = url.hostname.replace(/^www\./, "");
  if (!YOUTUBE_HOSTS.has(host)) {
    return null;
  }

  const segments = url.pathname.split("/").filter(Boolean);

  // youtu.be/<id> is a bare short link to one video.
  if (host === "youtu.be") {
    const id = segments[0];
    return id && VIDEO_ID.test(id) ? embedVideo(id) : null;
  }

  // /channel/UC…[/live] embeds that channel's live stream.
  if (segments[0] === "channel" && segments[1] && CHANNEL_ID.test(segments[1])) {
    return `https://www.youtube.com/embed/live_stream?channel=${segments[1]}`;
  }

  // /embed/<id>, /live/<id>, /shorts/<id> each name one video.
  if (
    (segments[0] === "embed" ||
      segments[0] === "live" ||
      segments[0] === "shorts") &&
    segments[1] &&
    VIDEO_ID.test(segments[1])
  ) {
    return embedVideo(segments[1]);
  }

  // /watch?v=<id>
  const v = url.searchParams.get("v");
  if (v && VIDEO_ID.test(v)) {
    return embedVideo(v);
  }

  return null;
}

function embedVideo(id: string): string {
  return `https://www.youtube.com/embed/${id}`;
}

// safeExternalHref returns source_ref only when it is an http(s) URL, so the
// fallback link can never carry a javascript:/data: scheme from a poisoned or
// mistaken admin-supplied channel reference (a stored-XSS vector). A non-http(s)
// ref yields null and the caller renders plain text with no link.
function safeExternalHref(raw: string): string | null {
  try {
    const url = new URL(raw);
    return url.protocol === "https:" || url.protocol === "http:"
      ? url.href
      : null;
  } catch {
    return null;
  }
}

// YoutubeEmbed renders the official iframe for a youtube channel in a 16:9
// responsive frame. When no embed URL can be derived from the source_ref it
// shows a clear "open on YouTube" link to the source rather than a broken frame.
export function YoutubeEmbed({
  sourceRef,
  title,
}: {
  sourceRef: string;
  title: string;
}) {
  const { t } = useAppI18n();
  const embedUrl = youtubeEmbedUrl(sourceRef);

  if (!embedUrl) {
    const href = safeExternalHref(sourceRef);
    return (
      <div className="flex aspect-video w-full flex-col items-center justify-center gap-2 rounded-2xl border border-dashed border-black/15 bg-black/[0.03] p-4 text-center dark:border-white/15 dark:bg-white/5">
        <p className="text-sm text-ink/60 dark:text-paper/60">
          {t.tv.embed.unavailable}
        </p>
        {href ? (
          <a
            href={href}
            target="_blank"
            rel="noreferrer noopener"
            className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-bleu hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-sky-300 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
          >
            {t.tv.embed.openOnYoutube}
          </a>
        ) : null}
      </div>
    );
  }

  return (
    <div className="aspect-video w-full overflow-hidden rounded-2xl border border-black/10 bg-night shadow-lg shadow-bleu/5 dark:border-white/10 dark:shadow-black/40">
      <iframe
        src={embedUrl}
        title={title}
        loading="lazy"
        allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share"
        allowFullScreen
        className="h-full w-full border-0"
      />
    </div>
  );
}
