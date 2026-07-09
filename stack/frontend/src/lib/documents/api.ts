// Typed client for the documents library and upload API (stack/backend
// internal/handler/documents.go). A document has a durable UUID identity; the
// object key is never addressed by clients, who upload via a presigned request
// and read the PDF through a presigned GET the backend mints. Sentences are
// extracted in the browser and posted here; this client only carries them.
import { API_BASE, toApiError } from "@/lib/http";
import { normalizeMatch, type MatchWire } from "@/lib/fact-check/api";
import type { LiveClaim } from "@/lib/live/claims";
import type {
  ClaimVerdict,
  LiteralVerdict,
  ManipulationFlag,
  VerdictBasis,
  VerdictSource,
} from "@/lib/live/frames";
import type { ExtractedSentence } from "@/lib/pdf/segment";

export type DocumentStatus = "pending" | "ready" | "failed";
// The analysis lifecycle, orthogonal to the upload status: a ready document may
// be unanalysed (none), mid-run (analysing), or finished (complete/failed).
export type DocumentAnalysisStatus =
  | "none"
  | "analysing"
  | "complete"
  | "failed";

// DOCUMENT_CONTENT_TYPE is the only type the API accepts; the browser checks it
// before requesting an upload so an unsupported file fails fast and locally.
export const DOCUMENT_CONTENT_TYPE = "application/pdf";

export function isAcceptedDocumentType(type: string): boolean {
  return type === DOCUMENT_CONTENT_TYPE;
}

// DocumentRecord is one document's metadata and analysis state, as returned by
// the upload and extraction endpoints.
export type DocumentRecord = {
  id: string;
  title: string;
  status: DocumentStatus;
  analysisStatus: DocumentAnalysisStatus;
  analysisError: string;
  contentType: string;
  sizeBytes: number;
  pageCount: number;
  sentencesTotal: number;
  sentencesProcessed: number;
  analysisRuns: number;
  analyzedAt: string;
  createdAt: string;
  updatedAt: string;
};

// LibraryDocument is one library row: a document plus its verdict summary counts.
export type LibraryDocument = DocumentRecord & {
  credibleClaims: number;
  disputedClaims: number;
};

// PresignedRequest is a pre-authorized request the browser issues directly to
// object storage: it sends Method to URL replaying every header in Headers.
export type PresignedRequest = {
  url: string;
  method: string;
  headers: Record<string, string[]>;
};

// DocumentUploadTicket is a pending document record plus the write-once presigned
// PUT the browser uses to upload the PDF, and the extraction sentence cap so the
// client can reject an over-long document before the PUT.
export type DocumentUploadTicket = {
  documentId: string;
  objectKey: string;
  status: DocumentStatus;
  upload: PresignedRequest;
  maxSentences: number;
};

export type DocumentUploadRequestInput = {
  title: string;
  contentType: string;
  sizeBytes: number;
};

export type DocumentExtractionInput = {
  pageCount: number;
  sentences: ExtractedSentence[];
};

type DocumentWire = {
  id: string;
  title: string;
  status: DocumentStatus;
  analysis_status: DocumentAnalysisStatus;
  analysis_error?: string;
  content_type: string;
  size_bytes: number;
  page_count: number;
  sentences_total: number;
  sentences_processed: number;
  analysis_runs: number;
  analyzed_at?: string;
  created_at: string;
  updated_at: string;
};

type LibraryDocumentWire = DocumentWire & {
  credible_claims: number;
  disputed_claims: number;
};

type PresignedWire = {
  url: string;
  method: string;
  headers?: Record<string, string[]>;
};

type ListWire = { documents?: LibraryDocumentWire[] };
type UploadTicketWire = {
  document_id: string;
  object_key: string;
  status: DocumentStatus;
  upload: PresignedWire;
  max_sentences: number;
};

function normalizeDocument(wire: DocumentWire): DocumentRecord {
  return {
    id: wire.id,
    title: wire.title,
    status: wire.status,
    analysisStatus: wire.analysis_status,
    analysisError: wire.analysis_error ?? "",
    contentType: wire.content_type,
    sizeBytes: wire.size_bytes,
    pageCount: wire.page_count,
    sentencesTotal: wire.sentences_total,
    sentencesProcessed: wire.sentences_processed,
    analysisRuns: wire.analysis_runs,
    analyzedAt: wire.analyzed_at ?? "",
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
  };
}

function normalizeLibraryDocument(wire: LibraryDocumentWire): LibraryDocument {
  return {
    ...normalizeDocument(wire),
    credibleClaims: wire.credible_claims,
    disputedClaims: wire.disputed_claims,
  };
}

function normalizePresigned(wire: PresignedWire): PresignedRequest {
  return { url: wire.url, method: wire.method, headers: wire.headers ?? {} };
}

export async function listDocuments(
  signal?: AbortSignal,
): Promise<LibraryDocument[]> {
  const response = await fetch(`${API_BASE}/api/documents`, { signal });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ListWire;
  return (wire.documents ?? []).map(normalizeLibraryDocument);
}

export async function requestDocumentUpload(
  input: DocumentUploadRequestInput,
  signal?: AbortSignal,
): Promise<DocumentUploadTicket> {
  const response = await fetch(`${API_BASE}/api/documents/uploads`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      title: input.title,
      content_type: input.contentType,
      size_bytes: input.sizeBytes,
    }),
    signal,
  });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as UploadTicketWire;
  return {
    documentId: wire.document_id,
    objectKey: wire.object_key,
    status: wire.status,
    upload: normalizePresigned(wire.upload),
    maxSentences: wire.max_sentences,
  };
}

export async function ingestExtraction(
  id: string,
  input: DocumentExtractionInput,
  signal?: AbortSignal,
): Promise<DocumentRecord> {
  const response = await fetch(
    `${API_BASE}/api/documents/${encodeURIComponent(id)}/extraction`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        page_count: input.pageCount,
        sentences: input.sentences,
      }),
      signal,
    },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as DocumentWire;
  return normalizeDocument(wire);
}

// DocumentSkipReason is why a sentence produced no verdict: the check-worthiness
// gate found no verifiable claim, or the claim fell outside the reference corpus.
// An empty string means the sentence was analysed and did produce claims (or has
// not been reached yet), so the viewer keys "skipped" on a non-empty reason.
export type DocumentSkipReason = "" | "not_a_claim" | "not_covered";

// DocumentSentence is one analysed sentence in document order with the atomic
// claims it produced. Claims are the LiveClaim shape so the viewer renders them
// with the same verdict components the live path uses; a skipped or not-yet-
// reached sentence carries an empty claims array.
export type DocumentSentence = {
  seq: number;
  page: number;
  text: string;
  occurrence: number;
  skipReason: DocumentSkipReason;
  claims: LiveClaim[];
};

// DocumentAnalysis is the polling target: the document's current metadata and
// progress plus every sentence with its claims, so one fetch drives both the
// progress bar and the fact-check panel and a hard refresh resumes from it.
export type DocumentAnalysis = {
  document: DocumentRecord;
  sentences: DocumentSentence[];
};

// DocumentDetail is the viewer's one-time load: the document metadata and a
// presigned GET for the PDF. pdfUrl is null until the document is ready (the
// object may not exist in storage before then), so the viewer shows a note
// rather than pointing react-pdf at nothing.
export type DocumentDetail = {
  document: DocumentRecord;
  pdfUrl: string | null;
};

// The claim wire fields carry the same closed vocabularies as the live path
// (the backend's CHECK-constrained columns), so they are typed as those unions
// rather than raw strings, exactly as DocumentWire trusts status/analysis_status.
type DocumentClaimWire = {
  id: string;
  claim_id: string;
  text: string;
  status: "verified" | "error";
  source?: VerdictSource;
  verdict?: ClaimVerdict;
  basis?: VerdictBasis;
  literal?: LiteralVerdict;
  flags?: ManipulationFlag[];
  confidence: number;
  rationale?: string;
  citations: MatchWire[];
};

type DocumentSentenceWire = {
  seq: number;
  page: number;
  text: string;
  occurrence: number;
  skip_reason?: DocumentSkipReason;
  claims: DocumentClaimWire[];
};

type DocumentDetailWire = DocumentWire & { pdf?: PresignedWire };
type DocumentClaimsWire = {
  document: DocumentWire;
  sentences?: DocumentSentenceWire[];
};

// normalizeDocumentClaim maps one stored claim onto the LiveClaim the verdict
// components consume: snake_case to camelCase, citations through the shared match
// normalizer. sourceLabel/sourceUrl/skipReason/error are live-stream-only fields
// with no document analogue, so they stay absent.
function normalizeDocumentClaim(wire: DocumentClaimWire): LiveClaim {
  return {
    claimId: wire.claim_id,
    text: wire.text,
    status: wire.status,
    source: wire.source,
    verdict: wire.verdict,
    basis: wire.basis,
    literal: wire.literal,
    flags: wire.flags,
    confidence: wire.confidence,
    rationale: wire.rationale,
    matches: wire.citations.map(normalizeMatch),
  };
}

function normalizeDocumentSentence(
  wire: DocumentSentenceWire,
): DocumentSentence {
  return {
    seq: wire.seq,
    page: wire.page,
    text: wire.text,
    occurrence: wire.occurrence,
    skipReason: wire.skip_reason ?? "",
    claims: wire.claims.map(normalizeDocumentClaim),
  };
}

export async function getDocument(
  id: string,
  signal?: AbortSignal,
): Promise<DocumentDetail> {
  const response = await fetch(
    `${API_BASE}/api/documents/${encodeURIComponent(id)}`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as DocumentDetailWire;
  return {
    document: normalizeDocument(wire),
    pdfUrl: wire.pdf?.url ?? null,
  };
}

export async function getDocumentClaims(
  id: string,
  signal?: AbortSignal,
): Promise<DocumentAnalysis> {
  const response = await fetch(
    `${API_BASE}/api/documents/${encodeURIComponent(id)}/claims`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as DocumentClaimsWire;
  return {
    document: normalizeDocument(wire.document),
    sentences: (wire.sentences ?? []).map(normalizeDocumentSentence),
  };
}

// reanalyseDocument triggers a fresh analysis run over the document's stored
// sentences. The backend answers 202 and runs the job in the background; a 409
// (already analysing) or 503 (analysis disabled) surfaces as an ApiError the
// caller branches on, so a concurrent run is reported rather than duplicated.
export async function reanalyseDocument(
  id: string,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch(
    `${API_BASE}/api/documents/${encodeURIComponent(id)}/reanalyse`,
    { method: "POST", signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
}
