"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import {
  type Channel,
  type ChannelCreateInput,
  type ChannelUpdateInput,
  createChannel,
  deleteChannel,
  listChannels,
  updateChannel,
} from "@/lib/tv/api";
import { BackofficeTvChannelForm } from "./backoffice-tv-channel-form";

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string | null };

// ToggleField names the two capture switches; both PATCH a single boolean.
type ToggleField = "enabled" | "archiveEnabled";

// BackofficeTvSection is the admin-only channel control surface: a table of every
// channel with capture and archive toggles, a live badge, per-row edit/delete,
// and an add/edit form. It is the single place capture is turned on or off; the
// /tv page is consumption-only. The catalog loads on the client (riding the
// same-origin proxy) and is re-listed after a create, edit, or delete. Toggles
// are optimistic: the switch flips immediately, PATCHes, and rolls back visibly
// on error. loadChannels, create, update, and remove are injection seams so
// tests drive each path deterministically.
export function BackofficeTvSection({
  loadChannels = listChannels,
  create = createChannel,
  update = updateChannel,
  remove = deleteChannel,
}: {
  loadChannels?: (signal?: AbortSignal) => Promise<Channel[]>;
  create?: (input: ChannelCreateInput, signal?: AbortSignal) => Promise<Channel>;
  update?: (
    id: string,
    patch: ChannelUpdateInput,
    signal?: AbortSignal,
  ) => Promise<Channel>;
  remove?: (id: string, signal?: AbortSignal) => Promise<void>;
} = {}) {
  const { t } = useAppI18n();
  const copy = t.backoffice.tv.list;

  const [channels, setChannels] = useState<Channel[]>([]);
  const [listState, setListState] = useState<ListState>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);
  const [editing, setEditing] = useState<Channel | null>(null);
  // toggleErrors keeps the last failed toggle per channel id as data so a
  // rolled-back switch shows why it snapped back and re-labels on locale switch.
  const [toggleErrors, setToggleErrors] = useState<
    Record<string, string | null>
  >({});

  // loadChannels is an injection seam fixed at mount; reading it from a ref keeps
  // it out of the load effect's deps so an inline caller cannot loop.
  const loadChannelsRef = useRef(loadChannels);
  useEffect(() => {
    loadChannelsRef.current = loadChannels;
  });

  const refresh = useCallback(() => {
    setReloadToken((token) => token + 1);
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    loadChannelsRef
      .current(controller.signal)
      .then((loaded) => {
        if (controller.signal.aborted) {
          return;
        }
        setChannels(loaded);
        setListState({ status: "loaded" });
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        setListState({
          status: "error",
          message: err instanceof Error ? err.message : null,
        });
      });
    return () => controller.abort();
  }, [reloadToken]);

  const retry = useCallback(() => {
    setListState({ status: "loading" });
    setReloadToken((token) => token + 1);
  }, []);

  // handleToggle flips the field immediately (optimistic), PATCHes it, replaces
  // the row with the server's record on success, and rolls the row back to its
  // prior value with an inline reason on error.
  const handleToggle = useCallback(
    async (channel: Channel, field: ToggleField) => {
      const previous = channel[field];
      const nextValue = !previous;
      setToggleErrors((prev) => {
        if (!(channel.id in prev)) {
          return prev;
        }
        const rest = { ...prev };
        delete rest[channel.id];
        return rest;
      });
      setChannels((prev) =>
        prev.map((c) =>
          c.id === channel.id ? { ...c, [field]: nextValue } : c,
        ),
      );
      try {
        const updated = await update(channel.id, { [field]: nextValue });
        setChannels((prev) =>
          prev.map((c) => (c.id === channel.id ? updated : c)),
        );
      } catch (err) {
        setChannels((prev) =>
          prev.map((c) =>
            c.id === channel.id ? { ...c, [field]: previous } : c,
          ),
        );
        setToggleErrors((prev) => ({
          ...prev,
          [channel.id]: err instanceof Error ? err.message : null,
        }));
      }
    },
    [update],
  );

  const startEdit = useCallback((channel: Channel) => {
    setEditing(channel);
  }, []);

  const onSaved = useCallback(() => {
    setEditing(null);
    refresh();
  }, [refresh]);

  return (
    <div className="flex flex-col gap-4">
      <BackofficeTvChannelForm
        key={editing ? editing.id : "add"}
        editing={editing}
        create={create}
        update={update}
        onSaved={onSaved}
        onCancelEdit={() => setEditing(null)}
      />
      <div className="flex flex-col gap-2">
        <h4 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {copy.heading}
        </h4>
        <ChannelTable
          listState={listState}
          onRetry={retry}
          channels={channels}
          onToggle={handleToggle}
          onEdit={startEdit}
          remove={remove}
          onDeleted={refresh}
          toggleErrors={toggleErrors}
        />
      </div>
    </div>
  );
}

type ChannelTableProps = {
  listState: ListState;
  onRetry: () => void;
  channels: Channel[];
  onToggle: (channel: Channel, field: ToggleField) => void;
  onEdit: (channel: Channel) => void;
  remove: (id: string, signal?: AbortSignal) => Promise<void>;
  onDeleted: () => void;
  toggleErrors: Record<string, string | null>;
};

function ChannelTable({
  listState,
  onRetry,
  channels,
  onToggle,
  onEdit,
  remove,
  onDeleted,
  toggleErrors,
}: ChannelTableProps) {
  const { t } = useAppI18n();
  const copy = t.backoffice.tv.list;

  if (listState.status === "loading") {
    return (
      <ul
        role="status"
        aria-label={copy.loadingAria}
        className="flex flex-col gap-2"
      >
        {Array.from({ length: 4 }, (_, index) => (
          <li
            key={index}
            aria-hidden
            className="h-11 animate-pulse rounded-lg border border-black/10 bg-black/5 dark:border-white/10 dark:bg-white/10"
          />
        ))}
      </ul>
    );
  }
  if (listState.status === "error") {
    return (
      <div className="flex flex-col items-start gap-2">
        <p role="alert" className="text-sm text-rouge dark:text-rose-300">
          {listState.message === null
            ? copy.loadErrorFallback
            : formatTemplate(copy.loadError, { message: listState.message })}
        </p>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {copy.retry}
        </button>
      </div>
    );
  }
  if (channels.length === 0) {
    return (
      <p className="rounded-xl border border-dashed border-black/15 px-4 py-8 text-center text-sm text-ink/50 dark:border-white/15 dark:text-paper/50">
        {copy.empty}
      </p>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[48rem] border-collapse text-left text-sm">
        <thead>
          <tr className="border-b border-black/10 text-xs uppercase tracking-wide text-ink/50 dark:border-white/10 dark:text-paper/50">
            <th scope="col" className="py-2 pr-3 font-medium">
              {copy.columns.name}
            </th>
            <th scope="col" className="py-2 pr-3 font-medium">
              {copy.columns.slug}
            </th>
            <th scope="col" className="py-2 pr-3 font-medium">
              {copy.columns.source}
            </th>
            <th scope="col" className="py-2 pr-3 font-medium">
              {copy.columns.sourceRef}
            </th>
            <th scope="col" className="py-2 pr-3 font-medium">
              {copy.columns.enabled}
            </th>
            <th scope="col" className="py-2 pr-3 font-medium">
              {copy.columns.archive}
            </th>
            <th scope="col" className="py-2 pr-3 font-medium">
              {copy.columns.status}
            </th>
            <th scope="col" className="py-2 font-medium">
              {copy.columns.actions}
            </th>
          </tr>
        </thead>
        <tbody>
          {channels.map((channel) => (
            <ChannelRow
              key={channel.id}
              channel={channel}
              onToggle={onToggle}
              onEdit={onEdit}
              remove={remove}
              onDeleted={onDeleted}
              toggleError={toggleErrors[channel.id]}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

type ChannelRowProps = {
  channel: Channel;
  onToggle: (channel: Channel, field: ToggleField) => void;
  onEdit: (channel: Channel) => void;
  remove: (id: string, signal?: AbortSignal) => Promise<void>;
  onDeleted: () => void;
  toggleError: string | null | undefined;
};

function ChannelRow({
  channel,
  onToggle,
  onEdit,
  remove,
  onDeleted,
  toggleError,
}: ChannelRowProps) {
  const { t } = useAppI18n();
  const copy = t.backoffice.tv.list;
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null | undefined>(
    undefined,
  );

  const fireDelete = async () => {
    setDeleting(true);
    setDeleteError(undefined);
    try {
      await remove(channel.id);
      setConfirming(false);
      onDeleted();
    } catch (err) {
      setConfirming(false);
      setDeleting(false);
      setDeleteError(err instanceof Error ? err.message : null);
    }
  };

  return (
    <tr className="border-b border-black/5 align-top dark:border-white/5">
      <td className="py-2 pr-3 font-medium text-ink dark:text-paper">
        {channel.name}
      </td>
      <td className="py-2 pr-3 text-ink/70 dark:text-paper/70">
        {channel.slug}
      </td>
      <td className="py-2 pr-3 text-ink/70 dark:text-paper/70">
        {t.backoffice.tv.form.kinds[channel.sourceKind]}
      </td>
      <td className="max-w-[16rem] truncate py-2 pr-3 text-ink/60 dark:text-paper/60">
        {channel.sourceRef}
      </td>
      <td className="py-2 pr-3">
        <ToggleSwitch
          on={channel.enabled}
          onLabel={formatTemplate(copy.disableAria, { name: channel.name })}
          offLabel={formatTemplate(copy.enableAria, { name: channel.name })}
          onClick={() => onToggle(channel, "enabled")}
        />
      </td>
      <td className="py-2 pr-3">
        <ToggleSwitch
          on={channel.archiveEnabled}
          onLabel={formatTemplate(copy.archiveOffAria, { name: channel.name })}
          offLabel={formatTemplate(copy.archiveOnAria, { name: channel.name })}
          onClick={() => onToggle(channel, "archiveEnabled")}
        />
      </td>
      <td className="py-2 pr-3">
        <LiveBadge live={channel.live} liveLabel={copy.live} offLabel={copy.offline} />
        {toggleError !== undefined ? (
          <p role="alert" className="mt-1 text-xs text-rouge dark:text-rose-300">
            {toggleError === null
              ? copy.toggleErrorFallback
              : formatTemplate(copy.toggleError, { message: toggleError })}
          </p>
        ) : null}
      </td>
      <td className="py-2">
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => onEdit(channel)}
              className="rounded-md border border-black/10 bg-white px-2.5 py-1 text-xs font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
            >
              {copy.edit}
            </button>
            {confirming ? (
              <span className="flex flex-wrap items-center gap-2">
                <span className="text-xs text-ink/60 dark:text-paper/60">
                  {copy.confirm}
                </span>
                <button
                  type="button"
                  onClick={fireDelete}
                  disabled={deleting}
                  className="rounded-md border border-rouge/30 bg-rouge/5 px-2.5 py-1 text-xs font-medium text-rouge hover:bg-rouge/10 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-rouge dark:text-rose-300"
                >
                  {deleting ? copy.deleting : copy.confirmYes}
                </button>
                <button
                  type="button"
                  onClick={() => setConfirming(false)}
                  disabled={deleting}
                  className="rounded-md px-2.5 py-1 text-xs font-medium text-ink/60 hover:bg-black/5 disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-paper/60 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
                >
                  {copy.confirmNo}
                </button>
              </span>
            ) : (
              <button
                type="button"
                onClick={() => setConfirming(true)}
                className="rounded-md border border-black/10 bg-white px-2.5 py-1 text-xs font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
              >
                {copy.delete}
              </button>
            )}
          </div>
          {deleteError !== undefined ? (
            <p role="alert" className="text-xs text-rouge dark:text-rose-300">
              {deleteError === null
                ? copy.deleteErrorFallback
                : formatTemplate(copy.deleteError, { message: deleteError })}
            </p>
          ) : null}
        </div>
      </td>
    </tr>
  );
}

// ToggleSwitch is an accessible on/off control: a button with role="switch" and
// aria-checked reflecting the current value, so tests and assistive tech read the
// state directly.
function ToggleSwitch({
  on,
  onLabel,
  offLabel,
  onClick,
}: {
  on: boolean;
  onLabel: string;
  offLabel: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={on ? onLabel : offLabel}
      onClick={onClick}
      className={
        "relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag " +
        (on
          ? "bg-bleu-flag"
          : "bg-black/20 dark:bg-white/20")
      }
    >
      <span
        aria-hidden
        className={
          "inline-block h-4 w-4 transform rounded-full bg-white transition-transform " +
          (on ? "translate-x-4" : "translate-x-1")
        }
      />
    </button>
  );
}

// LiveBadge shows whether a capture feed is currently connected.
function LiveBadge({
  live,
  liveLabel,
  offLabel,
}: {
  live: boolean;
  liveLabel: string;
  offLabel: string;
}) {
  if (live) {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-emerald-500/10 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:text-emerald-300">
        <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
        {liveLabel}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-black/5 px-2 py-0.5 text-xs font-medium text-ink/50 dark:bg-white/10 dark:text-paper/50">
      <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-ink/30 dark:bg-paper/30" />
      {offLabel}
    </span>
  );
}
