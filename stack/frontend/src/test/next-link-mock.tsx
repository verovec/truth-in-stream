import type { AnchorHTMLAttributes } from "react";

// Renders next/link as a plain anchor so components using <Link> can be unit
// tested without mounting the App Router context.
export default function Link({
  href,
  children,
  ...props
}: { href: string } & AnchorHTMLAttributes<HTMLAnchorElement>) {
  return (
    <a href={href} {...props}>
      {children}
    </a>
  );
}
