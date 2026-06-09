export type PlaybackCommand =
  | { kind: "toggle-play" }
  | { kind: "seek-by"; seconds: number }
  | { kind: "volume-by"; delta: number }
  | { kind: "toggle-mute" }
  | { kind: "toggle-fullscreen" };

export type PlaybackKeyEvent = {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  target: EventTarget | null;
};

export type PlaybackMedia = {
  paused: boolean;
  currentTime: number;
  duration: number;
  volume: number;
  muted: boolean;
  play: () => unknown;
  pause: () => void;
};

const INTERACTIVE_ROLES = new Set([
  "button",
  "checkbox",
  "combobox",
  "link",
  "menuitem",
  "option",
  "radio",
  "searchbox",
  "slider",
  "spinbutton",
  "switch",
  "tab",
  "textbox",
]);

function isInteractiveTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  if (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    target instanceof HTMLButtonElement ||
    target instanceof HTMLMediaElement ||
    (target instanceof HTMLAnchorElement && target.hasAttribute("href"))
  ) {
    return true;
  }
  if (target.isContentEditable || target.tabIndex >= 0) {
    return true;
  }
  const role = target.getAttribute("role");
  return role !== null && INTERACTIVE_ROLES.has(role);
}

export function resolvePlaybackCommand(
  event: PlaybackKeyEvent,
): PlaybackCommand | null {
  if (event.ctrlKey || event.metaKey || event.altKey) {
    return null;
  }
  if (isInteractiveTarget(event.target)) {
    return null;
  }
  switch (event.key.length === 1 ? event.key.toLowerCase() : event.key) {
    case " ":
    case "k":
      return { kind: "toggle-play" };
    case "ArrowLeft":
      return { kind: "seek-by", seconds: -5 };
    case "ArrowRight":
      return { kind: "seek-by", seconds: 5 };
    case "j":
      return { kind: "seek-by", seconds: -10 };
    case "l":
      return { kind: "seek-by", seconds: 10 };
    case "ArrowUp":
      return { kind: "volume-by", delta: 0.1 };
    case "ArrowDown":
      return { kind: "volume-by", delta: -0.1 };
    case "m":
      return { kind: "toggle-mute" };
    case "f":
      return { kind: "toggle-fullscreen" };
    default:
      return null;
  }
}

export function applyPlaybackCommand(
  media: PlaybackMedia,
  command: Exclude<PlaybackCommand, { kind: "toggle-fullscreen" }>,
): void {
  switch (command.kind) {
    case "toggle-play":
      if (media.paused) {
        Promise.resolve(media.play()).catch(() => undefined);
      } else {
        media.pause();
      }
      break;
    case "seek-by": {
      const max = Number.isFinite(media.duration)
        ? media.duration
        : Number.POSITIVE_INFINITY;
      media.currentTime = Math.min(
        Math.max(media.currentTime + command.seconds, 0),
        max,
      );
      break;
    }
    case "volume-by":
      media.volume = Math.min(
        Math.max(Math.round((media.volume + command.delta) * 100) / 100, 0),
        1,
      );
      if (command.delta > 0) {
        media.muted = false;
      }
      break;
    case "toggle-mute":
      media.muted = !media.muted;
      break;
  }
}
