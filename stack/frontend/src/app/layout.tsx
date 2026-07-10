import type { Metadata } from "next";
import { geistMono, geistSans, spectral } from "@/lib/fonts";
import "./globals.css";

export const metadata: Metadata = {
  metadataBase: new URL("https://jeminforme.fr"),
  title: "jeminforme.fr",
  description:
    "Verification des faits en direct pour le debat politique francais et europeen.",
  // The bare domain redirects to a locale, but link unfurlers that do not follow
  // the redirect still get a share card from this default.
  openGraph: {
    title: "jeminforme.fr",
    description:
      "Verification des faits en direct pour le debat politique francais et europeen.",
    siteName: "jeminforme.fr",
    locale: "fr_FR",
    type: "website",
  },
};

export default function RootLayout({
  children,
  auth,
}: Readonly<{
  children: React.ReactNode;
  auth: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${geistMono.variable} ${spectral.variable} h-full antialiased`}
    >
      {/* overflow-x-hidden is the app-wide guard against horizontal scroll: a
          single long token in a verdict or source title can never push the
          viewport wider than the screen on mobile. */}
      <body className="flex min-h-full flex-col overflow-x-hidden font-sans">
        {children}
        {auth}
      </body>
    </html>
  );
}
