// Stub for pdf.js's optional Node-only `canvas` dependency, aliased in
// next.config.ts so Turbopack can resolve the browser bundle. The text
// extraction and viewer paths never touch the Node canvas.
const emptyModule = {};
export default emptyModule;
