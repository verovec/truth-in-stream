// Sign-out control. Logout is a full navigation to the /auth/logout route
// handler, which clears the httpOnly session cookies server-side and redirects on
// to Keycloak's RP-initiated logout so the Keycloak SSO session ends too. A link
// (not a fetch) is correct because the route responds with cross-site redirects
// the browser must follow. The min-height keeps the touch target comfortable on
// mobile. The label comes from the active locale's dictionary via the shell.
export function LogoutButton({ label }: { label: string }) {
  return (
    // eslint-disable-next-line @next/next/no-html-link-for-pages
    <a
      href="/auth/logout"
      className="inline-flex min-h-9 items-center rounded-full border border-black/10 px-3.5 py-1.5 text-sm font-medium text-ink/80 transition-colors hover:bg-black/5 hover:text-ink dark:border-white/15 dark:text-paper/80 dark:hover:bg-white/5 dark:hover:text-paper"
    >
      {label}
    </a>
  );
}
