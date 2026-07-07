// A thin bleu-blanc-rouge hairline. The recurring brand thread that carries the
// tricolore through the page without ever coloring a whole surface.
export function TricoloreRule({ className }: { className?: string }) {
  return (
    <div
      aria-hidden="true"
      className={`flex h-0.5 w-full overflow-hidden ${className ?? ""}`}
    >
      <span className="h-full flex-1 bg-bleu-flag" />
      <span className="h-full flex-1 bg-paper" />
      <span className="h-full flex-1 bg-rouge-flag" />
    </div>
  );
}
