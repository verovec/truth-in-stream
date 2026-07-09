"use client";

import Link from "next/link";
import type { LibraryDocument } from "@/lib/documents/api";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate, plural } from "@/lib/i18n/text";
import { DocumentStateBadge } from "./document-state-badge";

// DocumentCard is one library tile linking to the document viewer: the title, its
// page count, the one state badge that matters (upload or analysis lifecycle),
// and the credible / disputed verdict summary once analysed. The whole tile is
// the link, named by the document title, so the whole surface opens the viewer.
export function DocumentCard({ doc }: { doc: LibraryDocument }) {
  const { t, locale } = useAppI18n();
  const pages = formatTemplate(
    plural(locale, doc.pageCount, t.documents.pageCount),
    { count: doc.pageCount },
  );
  const analysed = doc.status === "ready" && doc.analysisStatus === "complete";
  return (
    <Link
      href={`/documents/${doc.id}`}
      aria-label={doc.title}
      className="flex flex-col gap-2 rounded-xl border border-black/10 bg-white p-4 transition-colors hover:border-bleu-flag/50 hover:bg-black/[0.02] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/10 dark:bg-white/5 dark:hover:border-sky-400/50 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
    >
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
    </Link>
  );
}
