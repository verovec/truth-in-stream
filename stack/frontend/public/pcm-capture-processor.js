// AudioWorklet processor for the live fact-check audio path. It runs on the
// audio render thread, forwards each block of the first (mono) channel to the
// main thread (which resamples it to 16 kHz PCM), and passes the audio straight
// through to its output. The pass-through is load-bearing: the node sits
// between the media element and the speakers (source -> worklet -> destination),
// so createMediaElementSource has rerouted the element's audio into this graph;
// without copying input to output the node emits silence and the video goes
// mute. Keeping the worklet a thin forwarder leaves the testable resample/encode
// logic in TypeScript (src/lib/live/pcm.ts). This is a plain script loaded via
// addModule; it cannot use imports.
class PcmCaptureProcessor extends AudioWorkletProcessor {
  process(inputs, outputs) {
    const input = inputs[0];
    const channel = input && input[0];
    if (channel && channel.length > 0) {
      // The input buffer is reused between calls; copy before transferring.
      this.port.postMessage(channel.slice(0));
    }
    // Pass the audio through to the speakers. Each output channel takes its
    // matching input channel, falling back to the first so a mono source still
    // fills a stereo output; with no input the output stays silent.
    const output = outputs[0];
    if (input && output) {
      for (let c = 0; c < output.length; c++) {
        const inChannel = input[c] || input[0];
        if (inChannel) {
          output[c].set(inChannel);
        }
      }
    }
    // Keep the processor alive while the source is connected.
    return true;
  }
}

registerProcessor("pcm-capture-processor", PcmCaptureProcessor);
