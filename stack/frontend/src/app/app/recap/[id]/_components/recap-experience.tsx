"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { ApiError } from "@/lib/http";
import {
  getVideoRecap,
  moveSheet,
  ORDER_PARAM,
  orderSheets,
  parseSheetOrder,
  type Recap,
  serializeSheetOrder,
} from "@/lib/video/recap";
import { RecapCover } from "./recap-cover";
import { RecapTable } from "./recap-table";
import { SheetOrderControl } from "./sheet-order-control";
import { SpeakerSheet } from "./speaker-sheet";

type LoadError = "notReady" | "notFound" | "failed";

type LoadState =
  | { status: "loading" }
  | { status: "error"; error: LoadError }
  | { status: "ready"; recap: Recap };

function classify(err: unknown): LoadError {
  if (err instanceof ApiError && err.status === 409) {
    return "notReady";
  }
  if (err instanceof ApiError && err.status === 404) {
    return "notFound";
  }
  return "failed";
}

// writeOrderToUrl mirrors the sheet order into the address bar without a
// navigation. Next.js integrates native replaceState with its router, so the
// URL stays the single place the order lives and survives a reload. The
// default order needs no parameter and removes it.
function writeOrderToUrl(labels: string[], isDefault: boolean) {
  const url = new URL(window.location.href);
  if (isDefault) {
    url.searchParams.delete(ORDER_PARAM);
  } else {
    url.searchParams.set(ORDER_PARAM, serializeSheetOrder(labels));
  }
  window.history.replaceState(null, "", url);
}

/**
 * RecapExperience loads the recap of a video and renders it as a report: the
 * cover, the recap table, then one sheet per principal, in that order. The
 * sheet order starts from the URL (initialOrder, read by the page) and every
 * change is written back to it. loadRecap is an injection seam for tests.
 */
export function RecapExperience({
  videoId,
  brandName,
  initialOrder = null,
  loadRecap = getVideoRecap,
}: {
  videoId: string;
  brandName: string;
  initialOrder?: string | null;
  loadRecap?: (id: string, signal?: AbortSignal) => Promise<Recap>;
}) {
  const { t } = useAppI18n();
  const [state, setState] = useState<LoadState>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);
  const [order, setOrder] = useState(() => parseSheetOrder(initialOrder));

  useEffect(() => {
    const controller = new AbortController();
    loadRecap(videoId, controller.signal)
      .then((recap) => {
        if (!controller.signal.aborted) {
          setState({ status: "ready", recap });
        }
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", error: classify(err) });
        }
      });
    return () => controller.abort();
  }, [videoId, attempt, loadRecap]);

  if (state.status === "loading") {
    return (
      <p role="status" className="text-sm text-ink/70 dark:text-paper/70">
        {t.recap.loading}
      </p>
    );
  }
  if (state.status === "error") {
    return (
      <div className="flex flex-col items-start gap-3">
        <p role="alert" className="text-sm text-rouge dark:text-rose-300">
          {t.recap.errors[state.error]}
        </p>
        <div className="flex flex-wrap gap-3 text-sm">
          {state.error === "failed" ? (
            <button
              type="button"
              onClick={() => {
                setState({ status: "loading" });
                setAttempt((n) => n + 1);
              }}
              className="rounded-md border border-black/10 bg-white px-3 py-1.5 font-medium text-ink/80 hover:bg-black/5 dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10"
            >
              {t.recap.retry}
            </button>
          ) : null}
          <BackLink />
        </div>
      </div>
    );
  }

  const { recap } = state;
  const sheets = orderSheets(recap.speakers, order);
  const defaultLabels = orderSheets(recap.speakers, []).map((s) => s.label);
  const move = (index: number, offset: number) => {
    const next = moveSheet(
      sheets.map((speaker) => speaker.label),
      index,
      offset,
    );
    setOrder(next);
    writeOrderToUrl(
      next,
      next.every((label, i) => label === defaultLabels[i]),
    );
  };

  return (
    <article className="flex flex-col gap-4">
      <div className="print:hidden">
        <BackLink />
      </div>
      <RecapCover recap={recap} principals={sheets} brandName={brandName} />
      <RecapTable recap={recap} principals={sheets} />
      <SheetOrderControl sheets={sheets} onMove={move} />
      {sheets.map((speaker) => (
        <SpeakerSheet key={speaker.label} speaker={speaker} />
      ))}
    </article>
  );
}

function BackLink() {
  const { t } = useAppI18n();
  return (
    <Link
      href="/app"
      className="text-sm text-bleu underline underline-offset-2 hover:no-underline dark:text-sky-300"
    >
      {t.recap.back}
    </Link>
  );
}
