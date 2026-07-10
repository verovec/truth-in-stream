// Shared helpers for the upload hooks (videos and documents), which derive a
// display title from a file name and surface a transport/backend failure's raw
// message. Keeping one copy means a fix - like the extension-only file name below
// - lands in both flows at once instead of drifting between two verbatim copies.

// deriveTitle turns a file name into a human title: the base name without its
// extension, trimmed, falling back to the caller's default when nothing usable
// remains. A dot at position 0 marks an extension-only name (".pdf") or a
// dotfile, which has no base, so it also falls back rather than showing ".pdf".
export function deriveTitle(fileName: string, fallback: string): string {
  const dot = fileName.lastIndexOf(".");
  const base = dot > 0 ? fileName.slice(0, dot) : dot === 0 ? "" : fileName;
  return base.trim() || fallback;
}

// failureMessage is the raw backend/transport message when the failure carried
// one, else null so the caller shows its localized generic fallback rather than
// a baked-in English string.
export function failureMessage(err: unknown): string | null {
  return err instanceof Error ? err.message : null;
}
