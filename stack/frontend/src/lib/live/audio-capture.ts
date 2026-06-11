// Browser Web Audio implementation of the capture port. It taps the playing
// video element with a MediaElementAudioSourceNode, forwards raw audio blocks
// from an AudioWorklet, and resamples them to 16 kHz signed-16-bit PCM frames
// for the live socket. The hook injects a fake in tests; this is browser-only
// and covered by the production build.
//
// A MediaElementAudioSourceNode can be created only once per element, so the
// graph is cached per element and reused across sessions (reconnects, seeks,
// video switches that keep the same <video>). Capture is halted with the
// AudioContext's own suspend(), which the spec guarantees stops the worklet's
// process() calls; resume() continues it.
import { framePcm16k } from "./pcm";
import type { AudioCapture } from "./ports";

// The processor module is a plain script in /public: AudioWorklet runs in a
// scope without bundler chunk loading, so the import.meta.url pattern is
// unreliable under Turbopack. It must be loaded once per context.
const workletUrl = "/pcm-capture-processor.js";
const processorName = "pcm-capture-processor";

type CaptureGraph = {
  context: AudioContext;
  forward: (frame: ArrayBuffer) => void;
};

const graphs = new WeakMap<HTMLMediaElement, CaptureGraph>();

async function buildGraph(
  element: HTMLMediaElement,
  graph: CaptureGraph,
): Promise<void> {
  const { context } = graph;
  await context.audioWorklet.addModule(workletUrl);
  const source = context.createMediaElementSource(element);
  const worklet = new AudioWorkletNode(context, processorName);
  worklet.port.onmessage = (event: MessageEvent) => {
    const samples = event.data as Float32Array;
    graph.forward(framePcm16k(samples, context.sampleRate));
  };
  // Inline the worklet between the source and the speakers so the video stays
  // audible after createMediaElementSource reroutes its output.
  source.connect(worklet);
  worklet.connect(context.destination);
}

export function createMediaElementCapture(
  element: HTMLMediaElement,
  onFrame: (frame: ArrayBuffer) => void,
): AudioCapture {
  let graph = graphs.get(element);
  if (!graph) {
    const context = new AudioContext();
    graph = { context, forward: onFrame };
    graphs.set(element, graph);
    buildGraph(element, graph).catch((err: unknown) => {
      // A failed audio graph degrades to no live analysis; it must not break
      // playback. The socket simply receives no audio.
      console.error("live audio capture unavailable", err);
    });
  } else {
    // Reused graph (new session): forward to the latest socket.
    graph.forward = onFrame;
  }
  const { context } = graph;
  return {
    resume: () => void context.resume(),
    suspend: () => void context.suspend(),
    stop: () => void context.suspend(),
  };
}
