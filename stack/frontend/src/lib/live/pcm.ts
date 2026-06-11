// PCM conversion for the live audio path. The browser captures Float32 samples
// from the playing video at the AudioContext's native rate (typically 44.1 or
// 48 kHz); Scribe v2 Realtime requires signed 16-bit little-endian mono PCM at
// 16 kHz (see stack/backend internal/transcribe/realtime.go). These pure
// functions do the resample and quantization on the main thread so the logic is
// unit-testable, keeping the AudioWorklet a thin Float32 forwarder.

// TARGET_SAMPLE_RATE is the rate Scribe v2 Realtime expects.
export const TARGET_SAMPLE_RATE = 16_000;

/**
 * Downsamples mono Float32 samples to 16 kHz with linear interpolation, the
 * standard quality for speech. Input already at the target rate is returned as
 * is. The native rate is never below 16 kHz in practice, so this only ever
 * decimates; output position i samples input position i * (rate / 16000),
 * interpolating between the two bracketing samples.
 */
export function downsampleTo16kHz(
  input: Float32Array,
  inputRate: number,
): Float32Array {
  if (inputRate === TARGET_SAMPLE_RATE) {
    return input;
  }
  const ratio = inputRate / TARGET_SAMPLE_RATE;
  const outLength = Math.floor(input.length / ratio);
  const out = new Float32Array(outLength);
  for (let i = 0; i < outLength; i++) {
    const position = i * ratio;
    const low = Math.floor(position);
    const high = Math.min(low + 1, input.length - 1);
    const fraction = position - low;
    out[i] = input[low] * (1 - fraction) + input[high] * fraction;
  }
  return out;
}

/**
 * Encodes mono Float32 samples in [-1, 1] to signed 16-bit little-endian PCM,
 * clamping out-of-range samples so a loud transient saturates instead of
 * wrapping to the opposite polarity.
 */
export function encodePcm16(input: Float32Array): ArrayBuffer {
  const buffer = new ArrayBuffer(input.length * 2);
  const view = new DataView(buffer);
  for (let i = 0; i < input.length; i++) {
    const clamped = Math.max(-1, Math.min(1, input[i]));
    // Asymmetric scale: positive full-scale is 32767, negative is -32768.
    const value = clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff;
    view.setInt16(i * 2, Math.round(value), true);
  }
  return buffer;
}

/**
 * Resamples a native-rate Float32 block to a 16 kHz signed 16-bit PCM frame
 * ready to send over the live WebSocket.
 */
export function framePcm16k(
  input: Float32Array,
  inputRate: number,
): ArrayBuffer {
  return encodePcm16(downsampleTo16kHz(input, inputRate));
}
