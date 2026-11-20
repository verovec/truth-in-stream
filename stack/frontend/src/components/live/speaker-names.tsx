"use client";

import { createContext, type ReactNode, useContext } from "react";

const NO_NAMES: Record<string, string> = {};

const SpeakerNamesContext = createContext<Record<string, string>>(NO_NAMES);

// SpeakerNamesProvider shares the label-to-name lookup of the video on screen
// with every surface that prints a speaker (the transcript, the credibility
// panel). Analysis frames keep their diarized letters; names are applied here,
// at render time. Without a provider - a live stream, a TV channel - the lookup
// is empty and every surface falls back to the letter.
export function SpeakerNamesProvider({
  names,
  children,
}: {
  names: Record<string, string>;
  children: ReactNode;
}) {
  return (
    <SpeakerNamesContext.Provider value={names}>
      {children}
    </SpeakerNamesContext.Provider>
  );
}

// useSpeakerNames returns the label-to-name lookup; a label absent from it has
// no resolved name.
export function useSpeakerNames(): Record<string, string> {
  return useContext(SpeakerNamesContext);
}
