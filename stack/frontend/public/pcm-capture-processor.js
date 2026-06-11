// AudioWorklet processor for the live fact-check audio path. It runs on the
// audio render thread and forwards each block of the first (mono) channel to
// the main thread, which resamples it to 16 kHz PCM. Keeping the worklet a thin
// forwarder leaves the testable resample/encode logic in TypeScript
// (src/lib/live/pcm.ts). This is a plain script loaded via addModule; it cannot
// use imports.
class PcmCaptureProcessor extends AudioWorkletProcessor {
  process(inputs) {
    const input = inputs[0];
    const channel = input && input[0];
    if (channel && channel.length > 0) {
      // The input buffer is reused between calls; copy before transferring.
      this.port.postMessage(channel.slice(0));
    }
    // Keep the processor alive while the source is connected.
    return true;
  }
}

registerProcessor("pcm-capture-processor", PcmCaptureProcessor);
