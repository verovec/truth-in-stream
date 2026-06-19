import { DebugSurface } from "@/components/debug/debug-surface";
import type { Role } from "@/lib/auth/token";

import { LibraryExperience } from "./library-experience";
import { LogoutButton } from "./logout-button";
import { SessionKeepalive } from "./session-keepalive";

// AppShell is the synchronous, testable app surface: the header, the library
// experience, the session keepalive, and the admin-gated debug reveal. The role
// and authentication flag are resolved from the verified Keycloak session by the
// async page wrapper and passed in, so the shell stays a pure function of its
// props and the role gate is unit-testable without a request. min-w-0 on the
// column lets the flex children shrink instead of forcing horizontal overflow on
// narrow viewports.
export function AppShell({
  role,
  authenticated,
}: {
  role: Role;
  authenticated: boolean;
}) {
  return (
    <div className="flex min-w-0 flex-1 flex-col bg-zinc-50 dark:bg-zinc-900">
      <header className="flex items-center justify-between gap-3 border-b border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-950 sm:px-6">
        <h1 className="truncate text-base font-semibold tracking-tight text-zinc-900 dark:text-zinc-50 sm:text-lg">
          Truth in Stream
        </h1>
        <LogoutButton />
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 p-4 sm:p-6">
        <LibraryExperience />
      </main>
      {authenticated && <SessionKeepalive />}
      <DebugSurface role={role} />
    </div>
  );
}
