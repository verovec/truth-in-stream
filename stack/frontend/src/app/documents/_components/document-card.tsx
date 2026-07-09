"use client";

import type { LibraryDocument } from "@/lib/documents/api";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate, plural } from "@/lib/i18n/text";
import { DocumentStateBadge } from "./document-state-badge";

// DocumentCard is one library tile: the document title, its page count, the one
// state badge that matters (upload or analysis lifecycle), and the credible /
// disputed verdict summary once analysed. The viewer link is added by the viewer
// card so the tile stays a static presentation until there is somewhere to open.
export function DocumentCard({ doc }: { doc: LibraryDocument }) {
  const { t, locale } = useAppI18n();
  const pages = formatTemplate(
    plural(locale, doc.pageCount, t.documents.pageCount),
    { count: doc.pageCount },
  );
  const analysed = doc.status === "ready" && doc.analysisStatus === "complete";
  return (
    <article className="flex flex-col gap-2 rounded-xl border border-black/10 bg-white p-4 dark:border-white/10 dark:bg-white/5">
      <div className="flex items-start justify-between gap-2">
        <h3 className="min-w-0 truncate font-medium text-ink dark:text-paper">
          {doc.title}
        </h3>
        <DocumentStateBadge
          status={doc.status}
          analysisStatus={doc.analysisStatus}
        />
      </div>
      <p className="text-xs text-ink/50 dark:text-paper/50">{pages}</p>
      {analysed && (doc.credibleClaims > 0 || doc.disputedClaims > 0) ? (
        <div className="flex flex-wrap items-center gap-2">
          {doc.credibleClaims > 0 ? (
            <span className="inline-flex items-center rounded-full bg-verdict-credible/10 px-2 py-0.5 text-[11px] font-semibold text-verdict-credible dark:bg-verdict-credible/15">
              {formatTemplate(
                plural(locale, doc.credibleClaims, t.documents.counts.credible),
                { count: doc.credibleClaims },
              )}
            </span>
          ) : null}
          {doc.disputedClaims > 0 ? (
            <span className="inline-flex items-center rounded-full bg-verdict-disputed/10 px-2 py-0.5 text-[11px] font-semibold text-verdict-disputed dark:bg-verdict-disputed/15">
              {formatTemplate(
                plural(locale, doc.disputedClaims, t.documents.counts.disputed),
                { count: doc.disputedClaims },
              )}
            </span>
          ) : null}
        </div>
      ) : null}
    </article>
  );
}
