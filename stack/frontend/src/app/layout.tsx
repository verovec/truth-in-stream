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
  title: "jeminforme.fr",
  description:
    "Verification des faits en direct pour le debat politique francais et europeen.",
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
      lang="fr"
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
