"use client";

import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  type LibraryVideo,
  submitYoutubeUrl as defaultSubmit,
} from "@/lib/video/api";
import { useAppI18n } from "@/components/i18n/app-i18n";

// looksLikeUrl is a light client gate: a non-empty token with a host and a dot,
// optionally scheme-prefixed. The backend stays the authority on what is a valid
// YouTube link; this only avoids a round trip for obvious nonsense.
function looksLikeUrl(value: string): boolean {
  return /^(https?:\/\/)?[^\s.]+\.[^\s]+$/.test(value);
}

// YoutubeUrlForm lets the operator add a video by pasting a YouTube link. It owns
// only the input, the in-flight flag, and the inline error; submit is injected so
// the data call is testable, and onAdded hands the returned record to the library.
// FormError keeps the failure as data (not a rendered string), so an
// already-shown error re-labels itself when the operator switches locales. A
// failed submit keeps the API's own message when it carried one.
type FormError = { kind: "invalid" } | { kind: "failed"; message: string | null };

export function YoutubeUrlForm({
  onAdded,
  submit = defaultSubmit,
}: {
  onAdded: (video: LibraryVideo) => void;
  submit?: (url: string, signal?: AbortSignal) => Promise<LibraryVideo>;
}) {
  const { t } = useAppI18n();
  const inputId = useId();
  const errorId = useId();
  const [url, setUrl] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<FormError | null>(null);

  // Abort an in-flight submit on unmount so it cannot write state after the form
  // is gone.
  const abortRef = useRef<AbortController | null>(null);
  useEffect(() => () => abortRef.current?.abort(), []);

  const onSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = url.trim();
    if (!trimmed || !looksLikeUrl(trimmed)) {
      setError({ kind: "invalid" });
      return;
    }
    const controller = new AbortController();
    abortRef.current = controller;
    setSubmitting(true);
    setError(null);
    try {
      const video = await submit(trimmed, controller.signal);
      if (controller.signal.aborted) {
        return;
      }
      setUrl("");
      setSubmitting(false);
      onAdded(video);
    } catch (err) {
      if (controller.signal.aborted) {
        return;
      }
      setSubmitting(false);
      setError({
        kind: "failed",
        message: err instanceof Error ? err.message : null,
      });
    }
  };

  return (
    // noValidate: the backend is the authority on a valid YouTube link, so native
    // type=url constraint validation must not pre-empt the submit or reject a
    // scheme-less link the backend would accept.
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-1.5">
      <label
        htmlFor={inputId}
        className="text-xs font-medium text-ink/70 dark:text-paper/70"
      >
        {t.youtube.label}
      </label>
      <div className="flex gap-2">
        <input
          id={inputId}
          type="url"
          inputMode="url"
          autoComplete="off"
          placeholder={t.youtube.placeholder}
          value={url}
          disabled={submitting}
          aria-invalid={error !== null}
          aria-describedby={error ? errorId : undefined}
          onChange={(event) => setUrl(event.currentTarget.value)}
          className="min-w-0 flex-1 rounded-md border border-black/15 bg-white px-3 py-1.5 text-sm text-ink placeholder:text-ink/35 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag disabled:opacity-60 dark:border-white/15 dark:bg-white/5 dark:text-paper dark:placeholder:text-paper/35 dark:focus-visible:outline-paper/60"
        />
        <button
          type="submit"
          disabled={submitting}
          className="rounded-md bg-bleu px-3 py-1.5 text-sm font-semibold text-paper transition-colors hover:bg-bleu/90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag disabled:cursor-not-allowed disabled:opacity-60 dark:focus-visible:outline-paper/60"
        >
          {submitting ? t.youtube.adding : t.youtube.add}
        </button>
      </div>
      {error ? (
        <p
          id={errorId}
          role="alert"
          className="text-xs text-rouge dark:text-rose-300"
        >
          {error.kind === "invalid"
            ? t.youtube.invalid
            : (error.message ?? t.youtube.failed)}
        </p>
      ) : null}
    </form>
  );
}
