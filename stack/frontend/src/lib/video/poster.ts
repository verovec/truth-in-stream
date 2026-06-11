// Deterministic poster art for library tiles. Server-side thumbnailing is out
// of scope for this card and per-item client frame capture would need each
// video's presigned URL loaded just to paint a grid, so each tile gets a stable
// gradient derived from its id plus a title monogram: dependency-free, instant,
// and visually distinct per video.

function hash(seed: string): number {
  let h = 5381;
  for (let i = 0; i < seed.length; i += 1) {
    h = ((h << 5) + h + seed.charCodeAt(i)) >>> 0;
  }
  return h;
}

export function posterGradient(seed: string): string {
  const h = hash(seed);
  const hue1 = h % 360;
  const hue2 = (hue1 + 40 + ((h >> 8) % 80)) % 360;
  return `linear-gradient(135deg, hsl(${hue1} 70% 45%), hsl(${hue2} 60% 28%))`;
}

export function posterInitials(title: string): string {
  const words = title.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) {
    return "?";
  }
  if (words.length === 1) {
    return words[0].slice(0, 2).toUpperCase();
  }
  return (words[0][0] + words[words.length - 1][0]).toUpperCase();
}
