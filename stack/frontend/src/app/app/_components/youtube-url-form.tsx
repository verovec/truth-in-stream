"use client";

import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  type LibraryVideo,
  submitYoutubeUrl as defaultSubmit,
} from "@/lib/video/api";

// looksLikeUrl is a light client gate: a non-empty token with a host and a dot,
// optionally scheme-prefixed. The backend stays the authority on what is a valid
// YouTube link; this only avoids a round trip for obvious nonsense.
function looksLikeUrl(value: string): boolean {
  return /^(https?:\/\/)?[^\s.]+\.[^\s]+$/.test(value);
}

const INVALID_MESSAGE = "Enter a YouTube link, e.g. https://youtu.be/…";

// YoutubeUrlForm lets the operator add a video by pasting a YouTube link. It owns
// only the input, the in-flight flag, and the inline error; submit is injected so
// the data call is testable, and onAdded hands the returned record to the library.
export function YoutubeUrlForm({
  onAdded,
  submit = defaultSubmit,
}: {
  onAdded: (video: LibraryVideo) => void;
  submit?: (url: string, signal?: AbortSignal) => Promise<LibraryVideo>;
}) {
  const inputId = useId();
  const errorId = useId();
  const [url, setUrl] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Abort an in-flight submit on unmount so it cannot write state after the form
  // is gone.
  const abortRef = useRef<AbortController | null>(null);
  useEffect(() => () => abortRef.current?.abort(), []);

  const onSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = url.trim();
    if (!trimmed || !looksLikeUrl(trimmed)) {
      setError(INVALID_MESSAGE);
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
      setError(err instanceof Error ? err.message : "Could not add this video.");
    }
  };

  return (
    // noValidate: the backend is the authority on a valid YouTube link, so native
    // type=url constraint validation must not pre-empt the submit or reject a
    // scheme-less link the backend would accept.
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-1.5">
      <label
        htmlFor={inputId}
        className="text-xs font-medium text-zinc-700 dark:text-zinc-300"
      >
        YouTube URL
      </label>
      <div className="flex gap-2">
        <input
          id={inputId}
          type="url"
          inputMode="url"
          autoComplete="off"
          placeholder="https://youtu.be/…"
          value={url}
          disabled={submitting}
          aria-invalid={error !== null}
          aria-describedby={error ? errorId : undefined}
          onChange={(event) => setUrl(event.currentTarget.value)}
          className="min-w-0 flex-1 rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm text-zinc-900 placeholder:text-zinc-400 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
        />
        <button
          type="submit"
          disabled={submitting}
          className="rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm font-medium text-zinc-800 hover:bg-zinc-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 disabled:cursor-not-allowed disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100 dark:hover:bg-zinc-800"
        >
          {submitting ? "Adding…" : "Add"}
        </button>
      </div>
      {error ? (
        <p
          id={errorId}
          role="alert"
          className="text-xs text-rose-700 dark:text-rose-300"
        >
          {error}
        </p>
      ) : null}
    </form>
  );
}
