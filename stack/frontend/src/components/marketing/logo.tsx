type LogoProps = {
  size?: number;
  className?: string;
  // Set when the mark sits next to the visible "jeminforme.fr" wordmark, so a
  // screen reader announces the brand once (from the text) rather than twice.
  decorative?: boolean;
};

// A verification mark: a checkmark whose descending leg is bleu and ascending
// leg is rouge, split by a paper notch, on a light chip. The tricolore reads as
// "verified, France" at a glance and stays crisp down to ~20px.
export function Logo({ size = 28, className, decorative = false }: LogoProps) {
  const a11y = decorative
    ? { "aria-hidden": true as const }
    : { role: "img" as const, "aria-label": "jeminforme.fr" };

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 40 40"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      className={className}
      {...a11y}
    >
      <rect
        x="1"
        y="1"
        width="38"
        height="38"
        rx="12"
        className="fill-paper stroke-black/10 dark:stroke-white/15"
        strokeWidth="1"
      />
      <path
        d="M11 20 L17 26.5"
        stroke="#0055A4"
        strokeWidth="4"
        strokeLinecap="round"
      />
      <path
        d="M18.7 25 L30 11"
        stroke="#EF4135"
        strokeWidth="4"
        strokeLinecap="round"
      />
    </svg>
  );
}
