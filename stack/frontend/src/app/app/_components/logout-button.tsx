"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";

export function LogoutButton() {
  const router = useRouter();
  const [failed, setFailed] = useState(false);

  async function handleClick() {
    setFailed(false);
    try {
      const res = await fetch("/api/logout", { method: "POST" });
      if (!res.ok) {
        setFailed(true);
        return;
      }
    } catch {
      setFailed(true);
      return;
    }
    // Return to the public landing page. Navigating to /login here would be
    // intercepted by the @auth/(.)login modal route and render the login modal
    // over the analyser instead of leaving it; the landing page is not
    // intercepted and is the natural signed-out destination.
    router.push("/");
    router.refresh();
  }

  return (
    <span className="flex items-center gap-3">
      {failed && (
        <span role="alert" className="text-sm text-red-700 dark:text-red-300">
          Sign out failed. Try again.
        </span>
      )}
      <button
        type="button"
        onClick={handleClick}
        className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm font-medium text-zinc-700 hover:bg-zinc-100 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-800"
      >
        Sign out
      </button>
    </span>
  );
}
