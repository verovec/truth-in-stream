"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";
import type { Channel } from "@/lib/tv/api";

// ChannelTile is one card in the channel grid: the channel name, an ON AIR badge
// when a capture feed is currently connected, and a disabled badge with a muted
// treatment when capture is off. A disabled channel is only ever shown to an
// admin (the grid filters it out for guests), so the greyed state is an
// admin-only affordance. The whole tile is a button that opens the channel view.
export function ChannelTile({
  channel,
  onSelect,
}: {
  channel: Channel;
  onSelect: (channel: Channel) => void;
}) {
  const { t } = useAppI18n();
  const disabled = !channel.enabled;
  return (
    <button
      type="button"
      onClick={() => onSelect(channel)}
      data-disabled={disabled ? "true" : undefined}
      className={`flex w-full flex-col items-start gap-2 rounded-xl border p-4 text-left transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60 ${
        disabled
          ? "border-black/10 bg-black/[0.03] opacity-60 hover:opacity-80 dark:border-white/10 dark:bg-white/5"
          : "border-black/10 bg-white hover:bg-black/5 dark:border-white/10 dark:bg-white/5 dark:hover:bg-white/10"
      }`}
    >
      <span className="flex w-full items-center justify-between gap-2">
        <span className="min-w-0 truncate text-base font-semibold text-ink dark:text-paper">
          {channel.name}
        </span>
        {channel.live ? (
          <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-rouge/10 px-2 py-0.5 text-xs font-semibold uppercase tracking-wide text-rouge dark:bg-rouge/20 dark:text-rose-300">
            <span
              aria-hidden
              className="h-1.5 w-1.5 rounded-full bg-rouge dark:bg-rose-300"
            />
            {t.tv.grid.onAir}
          </span>
        ) : null}
      </span>
      <span className="flex items-center gap-2 text-xs text-ink/50 dark:text-paper/50">
        <span className="uppercase tracking-wide">{channel.sourceKind}</span>
        {disabled ? (
          <span className="rounded-full bg-black/5 px-2 py-0.5 font-medium text-ink/50 dark:bg-white/10 dark:text-paper/50">
            {t.tv.grid.disabled}
          </span>
        ) : null}
      </span>
    </button>
  );
}
