import type { Dictionary } from "@/lib/i18n/dictionaries/fr";
import { BackofficeDocumentsSection } from "./backoffice-documents-section";
import { BackofficeTvSection } from "./backoffice-tv-section";
import { BackofficeVideosSection } from "./backoffice-videos-section";

type BackofficeCopy = Dictionary["app"]["backoffice"];

// BackofficeExperience is the operator work area: an intro and two labelled
// sections - videos, documents, and TV channels. The videos section carries its
// ingestion controls (uploader, YouTube form, and the delete-capable management
// list); the documents section carries the PDF uploader and its in-flight tiles;
// the TV channels section is the admin control surface that turns capture on or
// off. It stays a
// server component - the scaffold needs no interactivity and renders each
// section's client leaf as a child. The page's level-1 heading is the brand in
// the header, so the area title is a level-2 heading and the sections are
// level-3, keeping one heading outline per page.
export function BackofficeExperience({ copy }: { copy: BackofficeCopy }) {
  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-2">
        <h2 className="text-2xl font-semibold tracking-tight">{copy.heading}</h2>
        <p className="text-sm text-ink/70 dark:text-paper/70">{copy.intro}</p>
      </div>
      <section
        aria-labelledby="backoffice-videos-heading"
        className="flex flex-col gap-3"
      >
        <div className="flex flex-col gap-1">
          <h3
            id="backoffice-videos-heading"
            className="text-lg font-semibold"
          >
            {copy.videos.heading}
          </h3>
          <p className="text-sm text-ink/70 dark:text-paper/70">
            {copy.videos.description}
          </p>
        </div>
        <BackofficeVideosSection />
      </section>
      <section
        aria-labelledby="backoffice-documents-heading"
        className="flex flex-col gap-3"
      >
        <div className="flex flex-col gap-1">
          <h3
            id="backoffice-documents-heading"
            className="text-lg font-semibold"
          >
            {copy.documents.heading}
          </h3>
          <p className="text-sm text-ink/70 dark:text-paper/70">
            {copy.documents.description}
          </p>
        </div>
        <BackofficeDocumentsSection />
      </section>
      <section
        aria-labelledby="backoffice-tv-heading"
        className="flex flex-col gap-3"
      >
        <div className="flex flex-col gap-1">
          <h3 id="backoffice-tv-heading" className="text-lg font-semibold">
            {copy.tv.heading}
          </h3>
          <p className="text-sm text-ink/70 dark:text-paper/70">
            {copy.tv.description}
          </p>
        </div>
        <BackofficeTvSection />
      </section>
    </div>
  );
}
