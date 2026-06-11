import { describe, expect, test } from "vitest";
import { downsampleTo16kHz, encodePcm16, framePcm16k, TARGET_SAMPLE_RATE } from "./pcm";

describe("downsampleTo16kHz", () => {
  test("returns the input unchanged when already at the target rate", () => {
    const input = Float32Array.of(0, 0.5, -0.5, 1);
    expect(downsampleTo16kHz(input, TARGET_SAMPLE_RATE)).toBe(input);
  });

  test("decimates a 48 kHz block to a third of its length", () => {
    const input = new Float32Array(48);
    const out = downsampleTo16kHz(input, 48_000);
    expect(out.length).toBe(16);
  });

  test("preserves the first sample and interpolates between neighbours", () => {
    // 32 kHz -> 16 kHz is a clean 2:1 ratio: output index i maps to input 2i.
    const input = Float32Array.of(0, 1, 0, 1, 0, 1, 0, 1);
    const out = downsampleTo16kHz(input, 32_000);
    expect(out.length).toBe(4);
    expect(out[0]).toBeCloseTo(0, 5);
    expect(out[1]).toBeCloseTo(0, 5);
    expect(out[2]).toBeCloseTo(0, 5);
  });

  test("linearly interpolates a non-integer ratio", () => {
    // 24 kHz -> 16 kHz, ratio 1.5: output index 1 maps to input position 1.5,
    // the midpoint between input[1]=2 and input[2]=4 -> 3.
    const input = Float32Array.of(0, 2, 4, 6, 8, 10);
    const out = downsampleTo16kHz(input, 24_000);
    expect(out[0]).toBeCloseTo(0, 5);
    expect(out[1]).toBeCloseTo(3, 5);
    expect(out[2]).toBeCloseTo(6, 5);
  });
});

describe("encodePcm16", () => {
  test("maps the full-scale range to signed 16-bit little-endian", () => {
    const buffer = encodePcm16(Float32Array.of(0, 1, -1));
    const view = new DataView(buffer);
    expect(buffer.byteLength).toBe(6);
    expect(view.getInt16(0, true)).toBe(0);
    expect(view.getInt16(2, true)).toBe(32_767);
    expect(view.getInt16(4, true)).toBe(-32_768);
  });

  test("clamps samples beyond [-1, 1] instead of wrapping", () => {
    const view = new DataView(encodePcm16(Float32Array.of(2, -2)));
    expect(view.getInt16(0, true)).toBe(32_767);
    expect(view.getInt16(2, true)).toBe(-32_768);
  });
});

describe("framePcm16k", () => {
  test("downsamples then encodes to a 16-bit PCM frame", () => {
    const input = new Float32Array(48); // 48 samples at 48 kHz -> 16 at 16 kHz
    const buffer = framePcm16k(input, 48_000);
    expect(buffer.byteLength).toBe(32); // 16 samples * 2 bytes
  });
});
