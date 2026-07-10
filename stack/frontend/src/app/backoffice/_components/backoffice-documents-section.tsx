"use client";

import { useDocumentUploads } from "@/hooks/use-document-uploads";
import { useAppI18n } from "@/components/i18n/app-i18n";
import type { ExtractionResult } from "@/lib/pdf/extract";
import type { PutUploader } from "@/lib/video/upload";
import { DocumentUploadTile } from "@/app/documents/_components/document-upload-tile";
import { DocumentUploader } from "@/app/documents/_components/document-uploader";

// UPLOAD_GRID_CLASS lays the in-flight upload tiles out on the same responsive
// grid the documents catalog uses, so a PDF being ingested reads at the same
// scale it will once it lands on the documents page.
const UPLOAD_GRID_CLASS = "grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3";

// BackofficeDocumentsSection is the admin-only PDF ingestion surface: the drop
// zone and the in-flight upload tiles while each PDF is extracted in the browser,
// uploaded, and confirmed. A confirmed document surfaces on /documents (the
// reading surface for every authenticated user), so this section carries no
// catalog and no delete or reanalyse - those contextual admin actions stay on the
// document viewer. The uploader and extractor are injection seams so tests drive
// the extract/upload/confirm chain deterministically, mirroring the videos
// section. This section reuses the documents route's uploader and tile verbatim
// via cross-route import, the established backoffice reuse pattern.
export function BackofficeDocumentsSection({
  uploader,
  extractor,
}: {
  uploader?: PutUploader;
  extractor?: (file: File, signal?: AbortSignal) => Promise<ExtractionResult>;
} = {}) {
  const { t } = useAppI18n();
  // No onUploaded: a confirmed document lives on the documents page, and this
  // section keeps no catalog to prepend it to. The ready job is still pruned by
  // the hook, so only the working and error tiles linger here.
  const { jobs, startUploads, dismiss } = useDocumentUploads({
    uploader,
    extractor,
  });
  const inFlight = jobs.filter((job) => job.state.status !== "ready");

  return (
    <div className="flex flex-col gap-4">
      <DocumentUploader onFiles={startUploads} />
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.backoffice.documents.hint}
      </p>
      {inFlight.length > 0 ? (
        <ul className={UPLOAD_GRID_CLASS}>
          {inFlight.map((job) => (
            <li key={job.id}>
              <DocumentUploadTile job={job} onDismiss={dismiss} />
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
