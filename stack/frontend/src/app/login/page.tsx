import { BrandHeading } from "@/components/marketing/brand-heading";
import { TricoloreRule } from "@/components/marketing/tricolore-rule";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";
import { LoginForm } from "./_components/login-form";

export async function generateMetadata() {
  const locale = await resolveRequestLocale();
  const dict = await getDictionary(locale);
  return { title: dict.login.metaTitle };
}

export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ error?: string }>;
}) {
  const { error } = await searchParams;
  const locale = await resolveRequestLocale();
  const dict = await getDictionary(locale);
  return (
    <main
      lang={locale}
      className="flex flex-1 items-center justify-center bg-paper p-4 font-sans text-ink antialiased dark:bg-night dark:text-paper"
    >
      <div className="w-full max-w-sm overflow-hidden rounded-2xl border border-black/10 bg-white shadow-xl shadow-bleu/5 dark:border-white/10 dark:bg-white/5 dark:shadow-black/40">
        <TricoloreRule />
        <div className="p-6">
          <BrandHeading name={dict.brand.name} />
          <p className="mt-2 text-sm text-ink/60 dark:text-paper/60">
            {dict.login.intro}
          </p>
          <LoginForm error={error} copy={dict.login} />
        </div>
      </div>
    </main>
  );
}
