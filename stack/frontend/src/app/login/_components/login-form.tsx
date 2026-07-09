import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

// Keycloak sign-in entry point. Login is delegated to Keycloak via the
// server-side authorization-code + PKCE flow (the /auth/login route handler),
// which sets the httpOnly session cookies and lands the user in the app. This
// supersedes the former password form: the credentials, token exchange, and
// session live entirely server-side, so no secret ever reaches the browser.
//
// A plain link to /auth/login (not a fetch) is correct: the route responds with
// a redirect to Keycloak, and a full navigation lets the browser follow the
// cross-site authorize redirect and the callback back. The optional error query
// param surfaces a failed or expired login transaction; its copy comes from the
// active locale's dictionary via the server parent.
export function LoginForm({
  error,
  copy,
}: {
  error?: string;
  copy: Dictionary["login"];
}) {
  const message =
    error === "session"
      ? copy.errors.session
      : error === "exchange"
        ? copy.errors.exchange
        : undefined;

  return (
    <div className="mt-6 flex flex-col gap-4">
      {message && (
        <p
          role="alert"
          className="rounded-md border border-rouge/25 bg-rouge/5 px-3 py-2 text-sm text-rouge dark:border-rouge/40 dark:bg-rouge/15 dark:text-rose-300"
        >
          {message}
        </p>
      )}
      {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
      <a
        href="/auth/login"
        className="inline-flex min-h-11 items-center justify-center rounded-lg bg-bleu px-4 py-2.5 text-sm font-semibold text-paper transition-colors hover:bg-bleu/90"
      >
        {copy.signIn}
      </a>
    </div>
  );
}
