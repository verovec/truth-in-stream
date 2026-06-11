// Browser Web Audio implementation of the capture port. It taps the playing
// video element with a MediaElementAudioSourceNode, forwards raw audio blocks
// from an AudioWorklet, and resamples them to 16 kHz signed-16-bit PCM frames
// for the live socket. The hook injects a fake in tests; this is browser-only
// and covered by the production build.
//
// A MediaElementAudioSourceNode can be created only once per element, so the
// graph is cached per element and reused across sessions (reconnects, seeks,
// video switches that keep the same <video>).
//
// createMediaElementSource reroutes the element's audio through this graph, so
// the AudioContext is the only path from the video to the speakers. Ending an
// analysis session must therefore never suspend the context, or the still-
// playing video goes mute. Forwarding PCM to the socket is gated by a flag the
// session toggles; the context is only suspended when the operator pauses (the
// element is silent anyway) and resumed on play.
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
  // Gates forwarding to the socket. The worklet always passes audio through to
  // the speakers; this only controls whether the captured PCM is streamed.
  forwarding: boolean;
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
  // Gate on the main thread to keep the worklet a thin forwarder (its only job
  // is pass-through + posting blocks). The cost is that a torn-down session
  // whose video keeps playing still posts blocks we drop here; that is cheaper
  // than teaching the worklet a control protocol, and the common teardown
  // (reconnect) resumes forwarding within seconds.
  worklet.port.onmessage = (event: MessageEvent) => {
    if (!graph.forwarding) {
      return;
    }
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
    graph = { context, forward: onFrame, forwarding: false };
    graphs.set(element, graph);
    buildGraph(element, graph).catch((err: unknown) => {
      // A failed audio graph degrades to no live analysis; it must not break
      // playback. The socket simply receives no audio.
      console.error("live audio capture unavailable", err);
    });
  } else {
    // Reused graph (new session): forward to the latest socket, and re-gate so a
    // frame can never reach the new socket before this session calls resume() -
    // a prior session that tore down abruptly may have left forwarding on.
    graph.forward = onFrame;
    graph.forwarding = false;
  }
  // Alias into a const so the returned closures capture a non-undefined graph:
  // a captured `let` of a nullable type is not narrowed inside a deferred call.
  const liveGraph = graph;
  const { context } = liveGraph;
  return {
    resume: () => {
      liveGraph.forwarding = true;
      void context.resume();
    },
    suspend: () => {
      // Operator pause: the element is paused, so suspending the context is
      // safe and stops the worklet running over silence.
      liveGraph.forwarding = false;
      void context.suspend();
    },
    stop: () => {
      // Session teardown (ended/failed/reconnecting/seek) can fire while the
      // video keeps playing. Only stop forwarding; never suspend the context,
      // which would mute the playing video.
      liveGraph.forwarding = false;
    },
  };
}
