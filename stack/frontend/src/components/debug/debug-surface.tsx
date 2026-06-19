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

// DebugSurface renders nothing for a guest and, for an admin, a toggle that
// reveals the wiki-search probe. The probe itself is also dead-code-eliminated in
// production builds (it checks NODE_ENV), so the surface is admin-gated at
// runtime and absent from production bundles by construction.
export function DebugSurface({ role }: { role: Role }) {
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
        className="fixed bottom-3 left-3 z-50 rounded-md border border-amber-400/60 bg-white/95 px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-amber-700 shadow-lg backdrop-blur hover:bg-amber-50 dark:border-amber-500/40 dark:bg-zinc-900/95 dark:text-amber-400 dark:hover:bg-zinc-800"
      >
        {open ? "Hide debug" : "Debug"}
      </button>
      {open && <WikiSearchBar />}
    </>
  );
}
