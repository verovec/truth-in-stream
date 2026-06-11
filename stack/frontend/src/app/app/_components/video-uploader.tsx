"use client";

import { useId, useState, type DragEvent } from "react";
import { ACCEPTED_VIDEO_TYPES } from "@/lib/video/api";

const ACCEPT = ACCEPTED_VIDEO_TYPES.join(",");

// VideoUploader is the drop zone and file picker. The label wraps a visually
// hidden, keyboard-focusable file input, so clicking or activating the zone
// opens the picker while drag-and-drop is handled on the same surface.
export function VideoUploader({
  onFiles,
}: {
  onFiles: (files: File[]) => void;
}) {
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
      data-testid="upload-zone"
      onDragOver={(event) => {
        event.preventDefault();
        setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={onDrop}
      className={`flex cursor-pointer flex-col items-center justify-center gap-1 rounded-xl border-2 border-dashed px-4 py-8 text-center transition focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-sky-500 ${
        dragging
          ? "border-sky-500 bg-sky-50 dark:bg-sky-500/10"
          : "border-zinc-300 hover:border-zinc-400 dark:border-zinc-700 dark:hover:border-zinc-600"
      }`}
    >
      <span className="text-sm font-medium text-zinc-900 dark:text-zinc-100">
        Drag a video here, or click to choose
      </span>
      <span className="text-xs text-zinc-500 dark:text-zinc-400">
        MP4, WebM, OGG, or MOV
      </span>
      <input
        id={inputId}
        type="file"
        accept={ACCEPT}
        multiple
        aria-label="Upload a video"
        className="sr-only"
        onChange={(event) => {
          emit(event.currentTarget.files);
          event.currentTarget.value = "";
        }}
      />
    </label>
  );
}
