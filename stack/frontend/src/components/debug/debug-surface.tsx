"use client";

// Admin-gated reveal for the developer debug tooling. The role is resolved
// server-side from the verified Keycloak session and passed in as a prop, so a
// non-admin never receives the debug surface in their tree at all. This is a
// reveal only: the backend independently gates every debug endpoint on a verified
// admin token, so toggling or forging this prop can never unlock the backing
// behaviour - it only shows or hides the control.
import { useState } from "react";

import { WikiSearchBar } from "./wiki-search-bar";
import type { Role } from "@/lib/auth/token";
import { useAppI18n } from "@/components/i18n/app-i18n";

// DebugSurface renders nothing for a guest and, for an admin, a toggle that
// reveals the wiki-search probe. The probe itself is also dead-code-eliminated in
// production builds (it checks NODE_ENV), so the surface is admin-gated at
// runtime and absent from production bundles by construction.
export function DebugSurface({ role }: { role: Role }) {
  const { t } = useAppI18n();
  const [open, setOpen] = useState(false);

  if (role !== "admin") {
    return null;
  }

  return (
    <>
      <button
        type="button"
        aria-pressed={open}
        onClick={() => setOpen((value) => !value)}
        className="fixed bottom-3 left-3 z-50 rounded-md border border-verdict-flag/50 bg-paper/95 px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-verdict-flag shadow-lg backdrop-blur hover:bg-verdict-flag/10 dark:border-verdict-flag/40 dark:bg-night/95 dark:text-amber-300 dark:hover:bg-white/5"
      >
        {open ? t.debug.hide : t.debug.show}
      </button>
      {open && <WikiSearchBar />}
    </>
  );
}
