"use client";

import {
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
  type RefObject,
} from "react";

// MIN_RATIO/MAX_RATIO keep both panes usable: neither the subtitles nor the
// fact-checks can be dragged shut, only resized within these bounds.
const MIN_RATIO = 0.2;
const MAX_RATIO = 0.8;
// KEY_STEP is one arrow-key nudge, so the divider is operable without a pointer.
const KEY_STEP = 0.04;

function clampRatio(value: number): number {
  return Math.min(MAX_RATIO, Math.max(MIN_RATIO, value));
}

// SeparatorProps is the ARIA + event bundle spread onto the divider element so
// it is an operable, labelled separator widget rather than a bare div.
export type SeparatorProps = {
  role: "separator";
  "aria-orientation": "horizontal";
  "aria-label": string;
  "aria-valuenow": number;
  "aria-valuemin": number;
  "aria-valuemax": number;
  tabIndex: 0;
  onPointerDown: (event: PointerEvent<HTMLElement>) => void;
  onPointerMove: (event: PointerEvent<HTMLElement>) => void;
  onPointerUp: (event: PointerEvent<HTMLElement>) => void;
  onKeyDown: (event: KeyboardEvent<HTMLElement>) => void;
};

export type VerticalSplit = {
  containerRef: RefObject<HTMLDivElement | null>;
  topGrow: number;
  bottomGrow: number;
  separatorProps: SeparatorProps;
};

/**
 * Drives a draggable horizontal divider between two stacked panes. It returns a
 * ref for the panes' shared container, the flex-grow weights for the top and
 * bottom panes, and the props for the divider. Dragging the divider (pointer)
 * or nudging it (ArrowUp/ArrowDown) repartitions the container's height between
 * the two panes, clamped so neither pane closes. Pointer capture routes the
 * drag to the divider even when the pointer leaves it, so no window listeners
 * are needed. label names the widget for assistive tech.
 *
 * initialRatio is the top pane's default share. It favours the top pane so the
 * bottom pane is the minority by default; the operator can still drag or nudge
 * the divider to any split within the bounds.
 */
export function useVerticalSplit(
  label: string,
  initialRatio = 0.65,
): VerticalSplit {
  const containerRef = useRef<HTMLDivElement>(null);
  const draggingRef = useRef(false);
  const [ratio, setRatio] = useState(() => clampRatio(initialRatio));

  function ratioFromPointer(clientY: number): number {
    const el = containerRef.current;
    if (!el) {
      return ratio;
    }
    const rect = el.getBoundingClientRect();
    if (rect.height === 0) {
      return ratio;
    }
    return clampRatio((clientY - rect.top) / rect.height);
  }

  return {
    containerRef,
    topGrow: ratio,
    bottomGrow: 1 - ratio,
    separatorProps: {
      role: "separator",
      "aria-orientation": "horizontal",
      "aria-label": label,
      "aria-valuenow": Math.round(ratio * 100),
      "aria-valuemin": Math.round(MIN_RATIO * 100),
      "aria-valuemax": Math.round(MAX_RATIO * 100),
      tabIndex: 0,
      onPointerDown: (event) => {
        event.preventDefault();
        draggingRef.current = true;
        event.currentTarget.setPointerCapture?.(event.pointerId);
      },
      onPointerMove: (event) => {
        if (!draggingRef.current) {
          return;
        }
        setRatio(ratioFromPointer(event.clientY));
      },
      onPointerUp: (event) => {
        draggingRef.current = false;
        if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
          event.currentTarget.releasePointerCapture(event.pointerId);
        }
      },
      onKeyDown: (event) => {
        if (event.key === "ArrowUp") {
          event.preventDefault();
          setRatio((current) => clampRatio(current - KEY_STEP));
        } else if (event.key === "ArrowDown") {
          event.preventDefault();
          setRatio((current) => clampRatio(current + KEY_STEP));
        }
      },
    },
  };
}
