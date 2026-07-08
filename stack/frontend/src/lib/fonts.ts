import { Geist, Geist_Mono, Spectral } from "next/font/google";

// Single instantiation per font family, shared by every layout that needs it.
// next/font dedupes by module instance, so importing these from several places
// loads each family once; instantiating a family twice would double its
// preloads.

export const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

export const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

// Editorial serif by Production Type (Paris) for display headings only; the
// body stays on Geist. Loaded at the root so the marketing landing and the
// authenticated analyser share one face.
export const spectral = Spectral({
  subsets: ["latin"],
  weight: ["500", "600"],
  style: ["normal", "italic"],
  variable: "--font-spectral",
  display: "swap",
});
