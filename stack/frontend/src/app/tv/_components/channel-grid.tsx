"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";
import type { Role } from "@/lib/auth/token";
import type { Channel } from "@/lib/tv/api";
import { ChannelTile } from "./channel-tile";

const GRID_CLASS = "grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3";

// visibleChannels applies the role-gated visibility rule: an admin sees every
// channel (disabled ones greyed by the tile), while a guest sees only the
// enabled ones. The backend independently serves the full list to any
// authenticated user, so this is reveal-only chrome; a guest simply never sees a
// channel that captures nothing.
export function visibleChannels(channels: Channel[], role: Role): Channel[] {
  if (role === "admin") {
    return channels;
  }
  return channels.filter((channel) => channel.enabled);
}

// ChannelGrid renders the role-filtered channel tiles. Selecting a tile opens
// the channel view; the grid itself is pure presentation.
export function ChannelGrid({
  channels,
  role,
  onSelect,
}: {
  channels: Channel[];
  role: Role;
  onSelect: (channel: Channel) => void;
}) {
  const { t } = useAppI18n();
  const visible = visibleChannels(channels, role);
  if (visible.length === 0) {
    return (
      <p className="text-sm text-ink/60 dark:text-paper/60">{t.tv.grid.empty}</p>
    );
  }
  return (
    <ul aria-label={t.tv.grid.ariaLabel} className={GRID_CLASS}>
      {visible.map((channel) => (
        <li key={channel.id}>
          <ChannelTile channel={channel} onSelect={onSelect} />
        </li>
      ))}
    </ul>
  );
}
