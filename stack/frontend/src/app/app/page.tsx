import { getSession } from "@/lib/auth/session";

import { AppShell } from "./_components/app-shell";

// Thin async wrapper: it resolves the caller's session from the verified Keycloak
// session cookie and hands the synchronous, tested AppShell its props. Keeping the
// data read here and the markup in AppShell makes the shell unit-testable (an
// async Server Component cannot be rendered by the test runner).
export default async function Home() {
  const { role, authenticated } = await getSession();
  return <AppShell role={role} authenticated={authenticated} />;
}
