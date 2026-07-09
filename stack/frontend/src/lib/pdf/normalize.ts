// Shared PDF text normalization. The extractor (upload time) and the viewer's
// highlight anchoring (view time) MUST apply these exact rules to the same raw
// text, or a stored sentence will not substring-match the rendered text layer
// and its highlight silently fails to anchor. Keeping the rules in one module is
// what makes anchoring deterministic across the two engines.
//
// The three passes, in order:
//   1. NFKC - folds ligatures (ﬁ -> fi), compatibility forms, and composed
//      diacritics to a single canonical code-point sequence, so the two engines
//      never disagree on the bytes of a character.
//   2. Soft-hyphen removal - U+00AD is the invisible line-break hyphenation
//      marker PDFs insert to justify text; stripping it rejoins the word
//      ("inter{SHY}national" -> "international"). A hard hyphen (U+002D) is left
//      alone: in French it carries meaning (peut-être, arc-en-ciel, c'est-à-dire)
//      and joining a hyphen-broken compound would corrupt far more words than the
//      rare born-digital hard line-break hyphen it would fix - and born-digital
//      PDFs, the only kind accepted here, break with soft hyphens, not hard ones.
//   3. Whitespace collapse - every run of whitespace becomes one space and the
//      ends are trimmed, so line and item boundaries from extraction do not leak
//      into the stored text.
// A soft hyphen plus any whitespace that follows it: at a line break the PDF
// emits the marker then a newline, so consuming the trailing whitespace rejoins
// the word rather than leaving a space where the break was.
const SOFT_HYPHEN = /\u00AD\s*/g;
const WHITESPACE_RUN = /\s+/g;
// The single-character forms of the classes the two rules above match, used by
// the provenance-tracking pass so it applies the exact same rules character by
// character. Keeping them beside the rules they mirror is what makes the mapped
// normalization the same normalization, not a second one.
const WHITESPACE_CHAR = /\s/;
const COMBINING_MARK = /\p{M}/u;

// normalizeText applies the shared normalization to one raw string. It is pure
// and idempotent: normalizing an already-normalized string returns it unchanged.
export function normalizeText(raw: string): string {
  return raw
    .normalize("NFKC")
    .replace(SOFT_HYPHEN, "")
    .replace(WHITESPACE_RUN, " ")
    .trim();
}

// NormalizedText is normalizeText's output paired with a provenance map: for
// every character of `text`, `sourceIndex[i]` is the UTF-16 index into `raw` of
// the character that produced it. A character expanded by NFKC (a ligature) maps
// each of its outputs back to the one source character; a character dropped by
// soft-hyphen removal or whitespace collapse simply has no output and no entry.
// The map is what lets the highlight overlay walk a matched sentence back to the
// exact text-layer items and offsets it spans.
export type NormalizedText = {
  text: string;
  sourceIndex: number[];
};

// normalizeWithMap runs the identical NFKC -> soft-hyphen-removal ->
// whitespace-collapse -> trim pipeline as normalizeText while tracking each output
// character back to its source, so anchoring reads the exact same normalized text
// extraction stored (an equivalence the tests pin: normalizeWithMap(raw).text ===
// normalizeText(raw)). It stays here, next to normalizeText and reusing its rule
// definitions, so there is only ever one normalization in the codebase.
export function normalizeWithMap(raw: string): NormalizedText {
  // Stage 1 - NFKC folding per starter-plus-combining-marks cluster, recording
  // each output code unit's source. Clustering on combining marks keeps the
  // per-cluster fold identical to a whole-string normalize() for the born-digital
  // text this app ingests, while yielding the per-character provenance a single
  // whole-string normalize() cannot.
  let folded = "";
  const foldedSource: number[] = [];
  let clusterStart = -1;
  let cluster = "";
  let clusterHasMark = false;
  const flushCluster = () => {
    if (clusterStart < 0) {
      return;
    }
    const normalized = cluster.normalize("NFKC");
    // A mark-free cluster whose length NFKC preserves is a positional identity, so
    // each output code unit maps to its own source code unit - this keeps an astral
    // character's surrogate pair correctly two code units wide. A cluster carrying
    // combining marks collapses to the cluster start instead: NFKC's canonical
    // ordering can permute equal-length marks, so a positional map would misattribute
    // them. A length-changing fold (a ligature expanding, marks composing) collapses
    // too, since the per-character correspondence is then undefined.
    const positional = !clusterHasMark && normalized.length === cluster.length;
    for (let k = 0; k < normalized.length; k += 1) {
      folded += normalized[k];
      foldedSource.push(positional ? clusterStart + k : clusterStart);
    }
    cluster = "";
    clusterStart = -1;
    clusterHasMark = false;
  };
  for (let i = 0; i < raw.length; ) {
    const codePoint = raw.codePointAt(i);
    if (codePoint === undefined) {
      break;
    }
    const char = String.fromCodePoint(codePoint);
    if (clusterStart >= 0 && COMBINING_MARK.test(char)) {
      cluster += char;
      clusterHasMark = true;
    } else {
      flushCluster();
      clusterStart = i;
      cluster = char;
    }
    i += char.length;
  }
  flushCluster();

  // Stage 2 - soft-hyphen removal. The exact SOFT_HYPHEN rule (U+00AD plus any
  // whitespace that follows it) is mirrored character by character: each soft
  // hyphen and the line break trailing it are dropped so the word rejoins, while
  // the surviving characters keep their own provenance. A hard hyphen is left
  // untouched, matching normalizeText.
  const droppedBySoftHyphen = new Array<boolean>(folded.length).fill(false);
  for (let j = 0; j < folded.length; j += 1) {
    if (folded[j] !== "\u00AD") {
      continue;
    }
    droppedBySoftHyphen[j] = true;
    for (
      let k = j + 1;
      k < folded.length && WHITESPACE_CHAR.test(folded[k]);
      k += 1
    ) {
      droppedBySoftHyphen[k] = true;
    }
  }

  // Stage 3 - whitespace collapse then trim, on the same \s class. Each run of
  // whitespace becomes one space mapped to the run's first character; a leading or
  // trailing collapsed space is dropped, matching .trim().
  const output: string[] = [];
  const sourceIndex: number[] = [];
  let pendingSpaceSource = -1;
  for (let j = 0; j < folded.length; j += 1) {
    if (droppedBySoftHyphen[j]) {
      continue;
    }
    const char = folded[j];
    if (WHITESPACE_CHAR.test(char)) {
      if (pendingSpaceSource < 0) {
        pendingSpaceSource = foldedSource[j];
      }
      continue;
    }
    if (pendingSpaceSource >= 0) {
      if (output.length > 0) {
        output.push(" ");
        sourceIndex.push(pendingSpaceSource);
      }
      pendingSpaceSource = -1;
    }
    output.push(char);
    sourceIndex.push(foldedSource[j]);
  }
  return { text: output.join(""), sourceIndex };
}
