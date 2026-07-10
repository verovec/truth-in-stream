import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

type BackofficeCopy = Dictionary["app"]["backoffice"];

// BackofficeExperience is the operator work area: an intro and two labelled
// sections - videos and documents - that later cards fill with their ingestion
// controls. It is a server component: the empty scaffold needs no interactivity,
// and each section gains its own client leaf (uploader, management list) in the
// video- and document-ingestion cards. The page's level-1 heading is the brand
// in the header, so the area title is a level-2 heading and the sections are
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
      </section>
    </div>
  );
}
