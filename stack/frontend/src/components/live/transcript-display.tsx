"use client";

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";

// TranscriptDisplay is the viewer-facing display preference shared between the
// summary strip and the transcript: whether unverifiable claim highlights are
// shown. By default the transcript marks only corroborated and contradicted
// claim words; toggling the strip's Unverified stat reveals the unverifiable
// marks for inspection. This is pure display state - it never touches the
// analysis session or its reducers.
type TranscriptDisplay = {
  showUnverified: boolean;
  toggleUnverified: () => void;
};

// The default keeps consumers rendering without a provider (unit tests, a
// standalone strip): unverified marks stay hidden and the toggle is inert.
const TranscriptDisplayContext = createContext<TranscriptDisplay>({
  showUnverified: false,
  toggleUnverified: () => {},
});

// TranscriptDisplayProvider hosts the preference for one watch screen, mounted
// alongside the analysis provider so the strip (which owns the toggle) and the
// transcript (which gates its marks on it) read one shared value.
export function TranscriptDisplayProvider({
  children,
}: {
  children: ReactNode;
}) {
  const [showUnverified, setShowUnverified] = useState(false);
  const toggleUnverified = useCallback(
    () => setShowUnverified((current) => !current),
    [],
  );
  const value = useMemo(
    () => ({ showUnverified, toggleUnverified }),
    [showUnverified, toggleUnverified],
  );
  return (
    <TranscriptDisplayContext.Provider value={value}>
      {children}
    </TranscriptDisplayContext.Provider>
  );
}

export function useTranscriptDisplay(): TranscriptDisplay {
  return useContext(TranscriptDisplayContext);
}
