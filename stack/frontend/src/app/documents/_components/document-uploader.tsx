"use client";

import { useId, useState, type DragEvent } from "react";
import { DOCUMENT_CONTENT_TYPE } from "@/lib/documents/api";
import { useAppI18n } from "@/components/i18n/app-i18n";

// DocumentUploader is the admin drop zone and file picker. The label wraps a
// visually hidden, keyboard-focusable file input, so clicking or activating the
// zone opens the picker while drag-and-drop lands on the same surface. It only
// accepts PDFs; the upload hook re-validates and rejects a scanned PDF before
// any server call.
export function DocumentUploader({
  onFiles,
}: {
  onFiles: (files: File[]) => void;
}) {
  const { t } = useAppI18n();
  const inputId = useId();
  const [dragging, setDragging] = useState(false);

  const emit = (list: FileList | null) => {
    const files = list ? Array.from(list) : [];
    if (files.length > 0) {
      onFiles(files);
    }
  };

  const onDrop = (event: DragEvent<HTMLLabelElement>) => {
    event.preventDefault();
    setDragging(false);
    emit(event.dataTransfer?.files ?? null);
  };

  return (
    <label
      htmlFor={inputId}
      data-testid="document-upload-zone"
      onDragOver={(event) => {
        event.preventDefault();
        setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={onDrop}
      className={`flex cursor-pointer flex-col items-center justify-center gap-1 rounded-xl border-2 border-dashed px-4 py-8 text-center transition focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-bleu-flag dark:focus-within:outline-paper/60 ${
        dragging
          ? "border-bleu-flag bg-bleu/5 dark:border-sky-400 dark:bg-sky-400/10"
          : "border-black/15 hover:border-black/30 dark:border-white/15 dark:hover:border-white/30"
      }`}
    >
      <span className="text-sm font-medium text-ink dark:text-paper">
        {t.documents.uploader.prompt}
      </span>
      <span className="text-xs text-ink/50 dark:text-paper/50">
        {t.documents.uploader.formats}
      </span>
      <input
        id={inputId}
        type="file"
        accept={DOCUMENT_CONTENT_TYPE}
        multiple
        aria-label={t.documents.uploader.inputAria}
        className="sr-only"
        onChange={(event) => {
          emit(event.currentTarget.files);
          event.currentTarget.value = "";
        }}
      />
    </label>
  );
}
