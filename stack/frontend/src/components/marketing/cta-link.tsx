import Link from "next/link";

// The primary action of the marketing surface: a bleu pill link. Extracted so a
// brand change to the call to action lives in one place.
export function CtaLink({
  href,
  size = "md",
  className,
  children,
}: {
  href: string;
  size?: "md" | "sm";
  className?: string;
  children: React.ReactNode;
}) {
  const padding = size === "sm" ? "px-4 py-2" : "px-6 py-3";
  return (
    <Link
      href={href}
      className={`inline-flex items-center justify-center rounded-lg bg-bleu ${padding} text-sm font-semibold text-paper transition-colors hover:bg-bleu/90 ${className ?? ""}`}
    >
      {children}
    </Link>
  );
}
