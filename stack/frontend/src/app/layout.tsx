import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

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
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      {/* overflow-x-hidden is the app-wide guard against horizontal scroll: a
          single long token in a verdict or source title can never push the
          viewport wider than the screen on mobile. */}
      <body className="flex min-h-full flex-col overflow-x-hidden">
        {children}
        {auth}
      </body>
    </html>
  );
}
