// Typed client for the documents library and upload API (stack/backend
// internal/handler/documents.go). A document has a durable UUID identity; the
// object key is never addressed by clients, who upload via a presigned request
// and read the PDF through a presigned GET the backend mints. Sentences are
// extracted in the browser and posted here; this client only carries them.
import { API_BASE, toApiError } from "@/lib/http";
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
