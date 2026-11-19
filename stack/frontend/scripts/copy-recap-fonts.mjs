// Copy the typefaces the recap PDF embeds into public/fonts/recap/ so the
// browser can fetch them when a PDF is generated. next/font serves the same
// families to the page but hands out no file a PDF generator can read, so the
// static files come from the pinned `geist` and `@fontsource/spectral` packages
// (both SIL Open Font License). Run from predev/prebuild; the copies are
// gitignored build artifacts keyed to those package versions, and standalone
// output ships public/ automatically. The list of files is shared with the
// PDF code (src/lib/recap-pdf/font-files.json), so the two cannot drift.
import { copyFile, mkdir, readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const destDir = join(root, "public", "fonts", "recap");

const fonts = JSON.parse(
  await readFile(join(root, "src", "lib", "recap-pdf", "font-files.json"), "utf8"),
);

await mkdir(destDir, { recursive: true });
for (const font of fonts) {
  await copyFile(join(root, "node_modules", font.source), join(destDir, font.file));
}
console.log(`copied ${fonts.length} recap PDF fonts -> ${destDir}`);
