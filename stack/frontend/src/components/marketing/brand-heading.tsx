import { Logo } from "./logo";

// BrandHeading is the brand lockup as a page's level-1 heading: the tricolore
// mark plus the wordmark in an <h1>. Unlike Brand (a Link back to the marketing
// landing), this is inert - it names the surface it sits on without navigating,
// so it suits the authenticated app header and the login page, where a stray
// click must not leave the surface (on /app that would tear down a live-analysis
// session). The mark is decorative because the wordmark text already names the
// brand for assistive technology.
export function BrandHeading({
  name,
  className,
}: {
  name: string;
  className?: string;
}) {
  return (
    <div className={`inline-flex items-center gap-2.5 ${className ?? ""}`}>
      <Logo decorative size={30} />
      <h1 className="text-lg font-semibold tracking-tight text-ink dark:text-paper">
        {name}
      </h1>
    </div>
  );
}
