"use client";

import { LiveFactCheckPanel } from "@/app/app/_components/live-fact-check-panel";
import { LiveSpeakerCredibility } from "@/app/app/_components/live-speaker-credibility";
import { LiveSummaryStrip } from "@/app/app/_components/live-summary-strip";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { ChannelLiveProvider } from "@/components/live/channel-live-provider";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import { formatTemplate } from "@/lib/i18n/text";
import type { LiveSocketFactory } from "@/lib/live/ports";
import type { Channel } from "@/lib/tv/api";
import type { PlayableVideo } from "@/lib/video/api";
import type { Recording } from "@/lib/tv/api";
import { RecordingsStrip } from "./recordings-strip";
import { YoutubeEmbed } from "./youtube-embed";

// ChannelView is the single-channel consumption surface: when the channel is on
// air it drives a read-only viewer session and shows the live panels beside the
// YouTube embed (youtube channels) or on their own (hls channels, which have no
// in-page player); when it is off air it says so plainly. Either way it lists
// the channel's archived recordings for replay. The live session and the player
// are the only client concerns here - there are no management controls (those
// are the backoffice). socketFactory, loadRecordings, and resolveRecording are
// injection seams for tests.
export function ChannelView({
  channel,
  socketFactory,
  loadRecordings,
  resolveRecording,
}: {
  channel: Channel;
  socketFactory?: LiveSocketFactory;
  loadRecordings?: (
    channelId: string,
    signal?: AbortSignal,
  ) => Promise<Recording[]>;
  resolveRecording?: (
    id: string,
    signal?: AbortSignal,
  ) => Promise<PlayableVideo>;
}) {
  return (
    <div className="flex flex-col gap-6">
      {channel.live ? (
        <LiveChannelStage channel={channel} socketFactory={socketFactory} />
      ) : (
        <OffAirNotice name={channel.name} />
      )}
      <RecordingsStrip
        channelId={channel.id}
        loadRecordings={loadRecordings}
        resolveRecording={resolveRecording}
      />
    </div>
  );
}

function LiveChannelStage({
  channel,
  socketFactory,
}: {
  channel: Channel;
  socketFactory?: LiveSocketFactory;
}) {
  const { t } = useAppI18n();
  return (
    <PlaybackProvider>
      {/* One read-only viewer session feeds the summary strip, the speaker
          credibility widget, and the fact-check panel: the provider owns the
          single WebSocket and publishes to a store the panels read
          independently, so a frame re-renders only the panels, never the embed
          or the recordings strip. */}
      <ChannelLiveProvider channelId={channel.id} socketFactory={socketFactory}>
        <div className="flex flex-col gap-4">
          <LiveSummaryStrip />
          <LiveSpeakerCredibility />
          <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
            <div className="flex flex-col gap-3">
              {channel.sourceKind === "youtube" ? (
                <YoutubeEmbed sourceRef={channel.sourceRef} title={channel.name} />
              ) : (
                <HlsSourceNote />
              )}
              <p className="text-xs text-ink/50 dark:text-paper/50">
                {t.tv.channel.analysisDelay}
              </p>
            </div>
            <LiveFactCheckPanel />
          </div>
        </div>
      </ChannelLiveProvider>
    </PlaybackProvider>
  );
}

// HlsSourceNote stands in for the player on an hls channel (a parliamentary
// stream): there is no in-page player, only the live analysis panels.
function HlsSourceNote() {
  const { t } = useAppI18n();
  return (
    <div className="flex aspect-video w-full items-center justify-center rounded-2xl border border-dashed border-black/15 bg-black/[0.03] p-4 text-center text-sm text-ink/60 dark:border-white/15 dark:bg-white/5 dark:text-paper/60">
      {t.tv.channel.hlsNoPlayer}
    </div>
  );
}

// OffAirNotice plainly states an off-air channel; its recordings still list
// below it (rendered by ChannelView).
function OffAirNotice({ name }: { name: string }) {
  const { t } = useAppI18n();
  return (
    <div
      role="status"
      className="flex flex-col items-center justify-center gap-1 rounded-2xl border border-black/10 bg-black/[0.03] p-8 text-center dark:border-white/10 dark:bg-white/5"
    >
      <p className="text-sm font-medium text-ink dark:text-paper">
        {formatTemplate(t.tv.channel.offAir, { name })}
      </p>
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.tv.channel.offAirHint}
      </p>
    </div>
  );
}
