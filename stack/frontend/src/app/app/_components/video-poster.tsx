import type { ReactNode } from "react";
import { posterGradient, posterInitials } from "@/lib/video/poster";

// VideoPoster paints a stable, generated thumbnail for a library tile: a
// gradient seeded by the video id with the title monogram. children overlays
// badges or progress on top of the art.
export function VideoPoster({
  seed,
  title,
  children,
}: {
  seed: string;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div
      className="relative flex aspect-video w-full items-center justify-center overflow-hidden"
      style={{ backgroundImage: posterGradient(seed) }}
    >
      <span
        aria-hidden
        className="text-3xl font-bold tracking-tight text-white/90 drop-shadow-sm"
      >
        {posterInitials(title)}
      </span>
      {children}
    </div>
  );
}
