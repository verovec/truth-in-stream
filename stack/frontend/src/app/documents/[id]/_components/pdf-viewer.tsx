"use client";

// This module is loaded only in the browser via next/dynamic({ ssr: false }):
// pdf.js touches browser globals (DOMMatrix) that do not exist during server
// render, so it must never be imported on the server. Importing the pdfjs config
// module configures the worker (served from public/) on the same pinned build as
// the viewer. The text-layer and annotation-layer stylesheets align the selectable
// text spans with the rendered canvas, which the in-PDF highlight card depends on.
import "@/lib/pdf/pdfjs";
import "react-pdf/dist/Page/TextLayer.css";
import "react-pdf/dist/Page/AnnotationLayer.css";

import { useEffect, useRef, useState } from "react";
import { Document, Page } from "react-pdf";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";

// PdfViewer renders every page of the PDF in a single scrollable column. Pages
// are sized to the measured container width so the document is responsive and the
// text layer stays aligned with the canvas at any width.
export default function PdfViewer({ url }: { url: string }) {
  const { t } = useAppI18n();
  const containerRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState<number | undefined>(undefined);
  const [numPages, setNumPages] = useState(0);

  useEffect(() => {
    const element = containerRef.current;
    if (!element) {
      return;
    }
    const measure = () => setWidth(element.clientWidth);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  return (
    <div
      ref={containerRef}
      className="flex flex-col items-center gap-4 rounded-xl border border-black/10 bg-black/5 p-3 dark:border-white/10 dark:bg-white/5"
    >
      <Document
        file={url}
        aria-label={t.viewer.pdf.documentAria}
        onLoadSuccess={(pdf) => setNumPages(pdf.numPages)}
        loading={
          <p className="py-8 text-sm text-ink/60 dark:text-paper/60">
            {t.viewer.pdf.loading}
          </p>
        }
        error={
          <p className="py-8 text-sm text-rouge dark:text-rose-300">
            {t.viewer.pdf.error}
          </p>
        }
        className="flex w-full flex-col items-center gap-4"
      >
        {Array.from({ length: numPages }, (_value, index) => (
          <Page
            key={index + 1}
            pageNumber={index + 1}
            width={width}
            aria-label={formatTemplate(t.viewer.pdf.page, { page: index + 1 })}
            className="max-w-full overflow-hidden rounded-md shadow-sm"
          />
        ))}
      </Document>
    </div>
  );
}
