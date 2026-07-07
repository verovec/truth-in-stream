import { Spectral } from "next/font/google";
import { notFound } from "next/navigation";
import { isLocale, locales } from "@/lib/i18n/config";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { Footer } from "./_components/footer";
import { Header } from "./_components/header";

// Editorial serif by Production Type (Paris) for display headings only; the
// body stays on Geist. Scoped here so it never loads outside the marketing
// surface.
const spectral = Spectral({
  subsets: ["latin"],
  weight: ["500", "600"],
  style: ["normal", "italic"],
  variable: "--font-spectral",
  display: "swap",
});

export function generateStaticParams() {
  return locales.map((locale) => ({ locale }));
}

export default async function LocaleLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ locale: string }>;
}) {
  const { locale } = await params;
  if (!isLocale(locale)) {
    notFound();
  }

  const dict = await getDictionary(locale);

  // The shared root layout renders <html lang="en"> for the authenticated app.
  // Setting lang here labels the marketing subtree with its actual locale for
  // assistive technology, server-side and per locale (WCAG "language of parts").
  // Cross-locale SEO is handled by the hreflang alternates in the page metadata.
  return (
    <div
      lang={locale}
      className={`${spectral.variable} flex flex-1 flex-col bg-paper font-sans text-ink antialiased dark:bg-night dark:text-paper`}
    >
      <Header locale={locale} dict={dict} />
      {children}
      <Footer locale={locale} dict={dict} />
    </div>
  );
}
