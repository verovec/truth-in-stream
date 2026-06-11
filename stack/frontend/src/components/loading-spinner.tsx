// LoadingSpinner is the app's single spinning-ring indicator, centered over its
// nearest positioned ancestor. size picks the ring: "sm" suits a library tile,
// "lg" the main player. It is decorative (aria-hidden) and never intercepts
// pointer events, so any native controls underneath stay clickable.
const SPINNER_SIZE = {
  sm: "h-6 w-6 border-2",
  lg: "h-10 w-10 border-[3px]",
} as const;

export function LoadingSpinner({ size }: { size: keyof typeof SPINNER_SIZE }) {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0 flex items-center justify-center"
    >
      <span
        className={`animate-spin rounded-full border-white/30 border-t-white ${SPINNER_SIZE[size]}`}
      />
    </div>
  );
}
