// Copy the pdf.js worker that react-pdf pins into public/ so it is served as a
// static asset. The bundler's asset-URL pattern for the worker is unreliable
// under Turbopack, and standalone output ships public/ automatically, so
// GlobalWorkerOptions.workerSrc points at "/pdf.worker.min.mjs". Run from
// predev/prebuild; the copied file is gitignored (it is a build artifact keyed
// to the pinned pdfjs-dist version).
import { copyFile, mkdir } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const publicDir = join(here, "..", "public");

// Resolve the worker through the pdfjs-dist react-pdf actually installed, so the
// worker version can never drift from the main-thread build.
const workerSrc = require.resolve("pdfjs-dist/build/pdf.worker.min.mjs");
const dest = join(publicDir, "pdf.worker.min.mjs");

await mkdir(publicDir, { recursive: true });
await copyFile(workerSrc, dest);
console.log(`copied pdf.js worker -> ${dest}`);
