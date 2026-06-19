// Keycloak sign-in entry point. Login is delegated to Keycloak via the
// server-side authorization-code + PKCE flow (the /auth/login route handler),
// which sets the httpOnly session cookies and lands the user in the app. This
// supersedes the former password form: the credentials, token exchange, and
// session live entirely server-side, so no secret ever reaches the browser.
//
// A plain link to /auth/login (not a fetch) is correct: the route responds with
// a redirect to Keycloak, and a full navigation lets the browser follow the
// cross-site authorize redirect and the callback back. The optional error query
// param surfaces a failed or expired login transaction.

const errorMessages: Record<string, string> = {
  session: "Your sign-in session expired. Please try again.",
  exchange: "Sign-in could not be completed. Please try again.",
};

export function LoginForm({ error }: { error?: string }) {
  const message = error ? errorMessages[error] : undefined;

  return (
    <div className="mt-6 flex flex-col gap-4">
      {message && (
        <p
          role="alert"
          className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300"
        >
          {message}
        </p>
      )}
      {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
      <a
        href="/auth/login"
        className="inline-flex min-h-11 items-center justify-center rounded-md bg-zinc-900 px-4 py-2.5 text-sm font-medium text-white hover:bg-zinc-700 dark:bg-zinc-100 dark:text-zinc-900 dark:hover:bg-zinc-300"
      >
        Sign in with Keycloak
      </a>
    </div>
  );
}
