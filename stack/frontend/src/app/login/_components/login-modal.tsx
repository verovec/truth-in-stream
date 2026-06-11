"use client";

import { useEffect, useRef } from "react";
import { useRouter } from "next/navigation";

const FOCUSABLE =
  'a[href],area[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),button:not([disabled]),[tabindex]:not([tabindex="-1"])';

const TITLE_ID = "login-modal-title";
const DESCRIPTION_ID = "login-modal-description";

// LoginModal is the dialog chrome for the intercepted /login route: it overlays
// the page it was opened from and closes back to it. The login form itself is
// passed as children so the modal owns only presentation and focus management,
// not the auth logic.
export function LoginModal({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    const dialog = dialogRef.current;
    dialog?.focus();

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        router.back();
        return;
      }
      if (event.key !== "Tab" || !dialog) {
        return;
      }
      const focusable = Array.from(
        dialog.querySelectorAll<HTMLElement>(FOCUSABLE),
      );
      if (focusable.length === 0) {
        event.preventDefault();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const active = document.activeElement;
      if (event.shiftKey && active === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      previouslyFocused?.focus?.();
    };
  }, [router]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-950/60 p-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          router.back();
        }
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={TITLE_ID}
        aria-describedby={DESCRIPTION_ID}
        tabIndex={-1}
        className="w-full max-w-sm rounded-lg border border-zinc-200 bg-white p-6 shadow-xl outline-none dark:border-zinc-800 dark:bg-zinc-950"
      >
        <div className="flex items-start justify-between">
          <div>
            <h2
              id={TITLE_ID}
              className="text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-50"
            >
              Sign in
            </h2>
            <p
              id={DESCRIPTION_ID}
              className="mt-1 text-sm text-zinc-500 dark:text-zinc-400"
            >
              Sign in to open the analyser
            </p>
          </div>
          <button
            type="button"
            onClick={() => router.back()}
            aria-label="Close"
            className="-mr-1 -mt-1 rounded-md p-1 text-zinc-400 hover:text-zinc-700 focus:outline-none focus:ring-2 focus:ring-zinc-300 dark:hover:text-zinc-200 dark:focus:ring-zinc-600"
          >
            <svg
              viewBox="0 0 20 20"
              fill="currentColor"
              aria-hidden="true"
              className="h-5 w-5"
            >
              <path d="M6.28 5.22a.75.75 0 0 0-1.06 1.06L8.94 10l-3.72 3.72a.75.75 0 1 0 1.06 1.06L10 11.06l3.72 3.72a.75.75 0 1 0 1.06-1.06L11.06 10l3.72-3.72a.75.75 0 0 0-1.06-1.06L10 8.94 6.28 5.22Z" />
            </svg>
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
