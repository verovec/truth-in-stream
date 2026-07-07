import Link from "next/link";
import type { Locale } from "@/lib/i18n/config";
import { Logo } from "./logo";

// The brand lockup: the tricolore mark plus the wordmark, linking home for the
// active locale. The mark is decorative here because the wordmark text already
// names the brand for assistive technology.
export function Brand({
  locale,
  name,
  className,
}: {
  locale: Locale;
  name: string;
  className?: string;
}) {
  return (
    <Link
      href={`/${locale}`}
      className={`group inline-flex items-center gap-2.5 ${className ?? ""}`}
    >
      <Logo decorative size={30} />
      <span className="text-lg font-semibold tracking-tight text-ink dark:text-paper">
        {name}
      </span>
    </Link>
  );
}
