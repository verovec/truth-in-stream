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

// The panel's copy stays in English on purpose: it is developer tooling, gated
// out of production builds above, so it is not part of the localized viewer
// chrome.
function WikiSearchPanel() {
  const { query, hits, error, connected, setQuery } = useDebugWikiSearch();

  return (
    <aside
      aria-label="Debug wiki search"
      className="fixed bottom-3 right-3 z-50 flex w-96 max-w-[calc(100vw-1.5rem)] flex-col gap-2 rounded-lg border border-verdict-flag/50 bg-paper/95 p-3 shadow-xl backdrop-blur dark:border-verdict-flag/40 dark:bg-night/95"
    >
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold uppercase tracking-wide text-verdict-flag dark:text-amber-300">
          Debug · embedded wiki search
        </span>
        <span
          className="flex items-center gap-1 text-xs text-ink/50 dark:text-paper/50"
          title={connected ? "connected" : "disconnected"}
        >
          <span
            aria-hidden
            className={`inline-block h-2 w-2 rounded-full ${
              connected ? "bg-verdict-credible" : "bg-verdict-unverifiable"
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
        className="w-full rounded-md border border-black/15 bg-white px-2 py-1.5 text-sm text-ink outline-none focus:border-verdict-flag dark:border-white/15 dark:bg-white/5 dark:text-paper"
      />

      {error && (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {error}
        </p>
      )}

      <ul className="flex max-h-80 flex-col gap-2 overflow-y-auto">
        {hits.length === 0 ? (
          <li className="py-2 text-center text-xs text-ink/40 dark:text-paper/40">
            {query.trim() === "" ? "Type to search" : "No matches"}
          </li>
        ) : (
          hits.map((hit, index) => (
            <li
              key={`${hit.url}:${index}`}
              className="rounded-md border border-black/10 p-2 dark:border-white/10"
            >
              <div className="flex items-baseline justify-between gap-2">
                <a
                  href={hit.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="truncate text-sm font-medium text-bleu hover:underline dark:text-sky-300"
                >
                  {hit.title}
                </a>
                <span className="shrink-0 font-mono text-xs text-ink/50 dark:text-paper/50">
                  {hit.similarity.toFixed(3)}
                </span>
              </div>
              <p className="mt-1 line-clamp-3 text-xs text-ink/60 dark:text-paper/70">
                {hit.snippet}
              </p>
            </li>
          ))
        )}
      </ul>
    </aside>
  );
}
