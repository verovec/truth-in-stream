import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { createMediaElementCapture } from "./audio-capture";

// The capture graph reroutes the video's audio through the AudioContext
// (source -> worklet -> destination), so the context is the only path to the
// speakers. These tests fake the Web Audio surface to lock the invariant that
// ending an analysis session never suspends that context: the video must stay
// audible even after the live socket tears the session down while playing.

class FakeAudioWorkletNode {
  port: { onmessage: ((event: { data: Float32Array }) => void) | null } = {
    onmessage: null,
  };

  constructor() {
    workletNodes.push(this);
  }

  connect(): void {}

  emit(samples: Float32Array): void {
    this.port.onmessage?.({ data: samples });
  }
}

class FakeAudioContext {
  state: "running" | "suspended" | "closed" = "suspended";
  sampleRate = 48_000;
  resumeCalls = 0;
  suspendCalls = 0;
  audioWorklet = { addModule: vi.fn(async () => {}) };
  destination = {};

  constructor() {
    contexts.push(this);
  }

  createMediaElementSource(): { connect: () => void } {
    return { connect: () => {} };
  }

  async resume(): Promise<void> {
    this.resumeCalls += 1;
    this.state = "running";
  }

  async suspend(): Promise<void> {
    this.suspendCalls += 1;
    this.state = "suspended";
  }
}

let workletNodes: FakeAudioWorkletNode[] = [];
let contexts: FakeAudioContext[] = [];

beforeEach(() => {
  workletNodes = [];
  contexts = [];
  vi.stubGlobal("AudioContext", FakeAudioContext);
  vi.stubGlobal("AudioWorkletNode", FakeAudioWorkletNode);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function build(onFrame: (frame: ArrayBuffer) => void) {
  const element = document.createElement("video") as unknown as HTMLMediaElement;
  const capture = createMediaElementCapture(element, onFrame);
  await vi.waitFor(() => expect(workletNodes.length).toBeGreaterThan(0));
  return {
    element,
    capture,
    node: workletNodes[workletNodes.length - 1],
    context: contexts[contexts.length - 1],
  };
}

const block = () => new Float32Array(128);

describe("createMediaElementCapture", () => {
  test("stop() keeps the audio context running so the video stays audible", async () => {
    const onFrame = vi.fn();
    const { capture, node, context } = await build(onFrame);

    capture.resume();
    node.emit(block());
    expect(onFrame).toHaveBeenCalledTimes(1);

    const stateBeforeStop = context.state;
    capture.stop();

    // The session ended, but suspending the context would mute the playing
    // video. stop() must not touch the context at all.
    expect(context.suspendCalls).toBe(0);
    expect(context.state).toBe(stateBeforeStop);
  });

  test("stop() stops forwarding frames to the socket", async () => {
    const onFrame = vi.fn();
    const { capture, node } = await build(onFrame);

    capture.resume();
    node.emit(block());
    expect(onFrame).toHaveBeenCalledTimes(1);

    capture.stop();
    node.emit(block());

    // No further frames reach the (now closed) socket.
    expect(onFrame).toHaveBeenCalledTimes(1);
  });

  test("resume() forwards frames and suspend() gates them", async () => {
    const onFrame = vi.fn();
    const { capture, node } = await build(onFrame);

    capture.resume();
    node.emit(block());
    expect(onFrame).toHaveBeenCalledTimes(1);

    capture.suspend();
    node.emit(block());
    expect(onFrame).toHaveBeenCalledTimes(1);

    capture.resume();
    node.emit(block());
    expect(onFrame).toHaveBeenCalledTimes(2);
  });

  test("does not forward frames before resume()", async () => {
    const onFrame = vi.fn();
    const { node } = await build(onFrame);

    node.emit(block());

    expect(onFrame).not.toHaveBeenCalled();
  });

  test("a reused graph starts gated and forwards to the latest socket", async () => {
    const first = vi.fn();
    const { element, capture, node, context } = await build(first);

    capture.resume();
    node.emit(block());
    expect(first).toHaveBeenCalledTimes(1);
    // No stop()/suspend(): simulate an abrupt teardown that leaves the prior
    // session forwarding, so reuse must re-gate rather than inherit the flag.

    // A new session on the SAME element reuses the one graph/context, because a
    // MediaElementAudioSourceNode can be built only once per element.
    const second = vi.fn();
    const reused = createMediaElementCapture(element, second);
    expect(context).toBe(contexts[0]);
    expect(contexts).toHaveLength(1);

    // The reused wrapper starts gated: a frame must not leak to the new socket
    // before resume() opens it.
    node.emit(block());
    expect(second).not.toHaveBeenCalled();

    reused.resume();
    node.emit(block());
    // Frames now reach the new socket only, never the previous session's.
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).toHaveBeenCalledTimes(1);
  });
});
