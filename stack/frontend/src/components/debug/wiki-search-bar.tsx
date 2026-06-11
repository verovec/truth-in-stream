"use client";

// Developer-only search bar over the embedded wiki corpus. It streams queries to
// the backend probe over a WebSocket (the same transport the live fact-check
// stream uses) and shows the raw nearest-neighbor hits. It renders only outside
// production builds, so it can never ship to users.
import { useDebugWikiSearch } from "@/hooks/use-debug-wiki-search";

export function WikiSearchBar() {
  // process.env.NODE_ENV is statically inlined at build time, so the panel and
  // its hook are dead code in a production bundle and never run.
  if (process.env.NODE_ENV === "production") {
    return null;
  }
  return <WikiSearchPanel />;
}

function WikiSearchPanel() {
  const { query, hits, error, connected, setQuery } = useDebugWikiSearch();

  return (
    <aside
      aria-label="Debug wiki search"
      className="fixed bottom-3 right-3 z-50 flex w-96 max-w-[calc(100vw-1.5rem)] flex-col gap-2 rounded-lg border border-amber-400/60 bg-white/95 p-3 shadow-xl backdrop-blur dark:border-amber-500/40 dark:bg-zinc-900/95"
    >
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold uppercase tracking-wide text-amber-700 dark:text-amber-400">
          Debug · embedded wiki search
        </span>
        <span
          className="flex items-center gap-1 text-xs text-zinc-500 dark:text-zinc-400"
          title={connected ? "connected" : "disconnected"}
        >
          <span
            aria-hidden
            className={`inline-block h-2 w-2 rounded-full ${
              connected ? "bg-green-500" : "bg-zinc-400"
            }`}
          />
          {connected ? "live" : "offline"}
        </span>
      </div>

      <input
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        placeholder="Search the embedded corpus…"
        aria-label="Wiki search query"
        className="w-full rounded-md border border-zinc-300 bg-white px-2 py-1.5 text-sm text-zinc-900 outline-none focus:border-amber-500 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
      />

      {error && (
        <p role="alert" className="text-xs text-red-700 dark:text-red-300">
          {error}
        </p>
      )}

      <ul className="flex max-h-80 flex-col gap-2 overflow-y-auto">
        {hits.length === 0 ? (
          <li className="py-2 text-center text-xs text-zinc-400 dark:text-zinc-500">
            {query.trim() === "" ? "Type to search" : "No matches"}
          </li>
        ) : (
          hits.map((hit, index) => (
            <li
              key={`${hit.url}:${index}`}
              className="rounded-md border border-zinc-200 p-2 dark:border-zinc-800"
            >
              <div className="flex items-baseline justify-between gap-2">
                <a
                  href={hit.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="truncate text-sm font-medium text-blue-700 hover:underline dark:text-blue-400"
                >
                  {hit.title}
                </a>
                <span className="shrink-0 font-mono text-xs text-zinc-500 dark:text-zinc-400">
                  {hit.similarity.toFixed(3)}
                </span>
              </div>
              <p className="mt-1 line-clamp-3 text-xs text-zinc-600 dark:text-zinc-300">
                {hit.snippet}
              </p>
            </li>
          ))
        )}
      </ul>
    </aside>
  );
}
