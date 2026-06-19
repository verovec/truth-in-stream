// Sign-out control. Logout is a full navigation to the /auth/logout route
// handler, which clears the httpOnly session cookies server-side and redirects on
// to Keycloak's RP-initiated logout so the Keycloak SSO session ends too. A link
// (not a fetch) is correct because the route responds with cross-site redirects
// the browser must follow. The min-height keeps the touch target comfortable on
// mobile.
export function LogoutButton() {
  return (
    // eslint-disable-next-line @next/next/no-html-link-for-pages
    <a
      href="/auth/logout"
      className="inline-flex min-h-9 items-center rounded-md border border-zinc-300 px-3 py-1.5 text-sm font-medium text-zinc-700 hover:bg-zinc-100 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-800"
    >
      Sign out
    </a>
  );
}
