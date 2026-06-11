import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = {
  title: "Truth in Stream - Source-backed fact-checking for live debate",
  description:
    "Truth in Stream checks political claims against cited sources in real time, so debates and live streams are anchored to the record as they happen.",
  openGraph: {
    title: "Truth in Stream - Source-backed fact-checking for live debate",
    description:
      "Real-time, source-backed fact-checking for political discourse. Claims are matched to cited evidence the moment they are spoken.",
    siteName: "Truth in Stream",
    type: "website",
  },
};

const STEPS = [
  {
    title: "Listen",
    body: "Live audio is transcribed as it is spoken, statement by statement, with no waiting for the stream to end.",
  },
  {
    title: "Match",
    body: "Each checkable claim is compared against a curated record of verified claims and cited reference material.",
  },
  {
    title: "Verdict",
    body: "A verdict surfaces within seconds, with its sources attached, so viewers see what holds up and what does not.",
  },
];

const CREDIBILITY = [
  { label: "Source-backed", body: "Every verdict carries the citations behind it." },
  { label: "Real time", body: "Claims are checked as they are made, not after." },
  {
    label: "On the record",
    body: "Opinions and small talk are left alone; only verifiable claims get a verdict.",
  },
];

export default function LandingPage() {
  return (
    <div className="flex flex-1 flex-col bg-white text-zinc-900 dark:bg-zinc-950 dark:text-zinc-50">
      <header className="mx-auto flex w-full max-w-6xl items-center justify-between px-6 py-5">
        <span className="text-lg font-semibold tracking-tight">
          Truth in Stream
        </span>
        <Link
          href="/login"
          className="rounded-md px-3 py-1.5 text-sm font-medium text-zinc-600 transition-colors hover:text-zinc-900 dark:text-zinc-300 dark:hover:text-zinc-50"
        >
          Sign in
        </Link>
      </header>

      <main className="flex flex-1 flex-col">
        <section className="mx-auto flex w-full max-w-6xl flex-col items-center px-6 pb-20 pt-16 text-center sm:pt-24">
          <p className="mb-5 inline-flex items-center rounded-full border border-zinc-200 bg-zinc-50 px-3 py-1 text-xs font-medium uppercase tracking-wide text-zinc-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
            Real-time fact-checking for live debate
          </p>
          <h1 className="max-w-3xl text-balance text-4xl font-semibold tracking-tight sm:text-5xl lg:text-6xl">
            Put the truth in the middle of the politics stage
          </h1>
          <p className="mt-6 max-w-2xl text-balance text-lg text-zinc-600 dark:text-zinc-300">
            Truth in Stream checks claims against cited sources the moment they
            are spoken. Debates and live streams stay anchored to the record,
            not to whoever talks loudest.
          </p>
          <div className="mt-9 flex flex-col items-center gap-3 sm:flex-row">
            <Link
              href="/login"
              className="rounded-md bg-zinc-900 px-6 py-3 text-sm font-medium text-white transition-colors hover:bg-zinc-700 dark:bg-zinc-50 dark:text-zinc-900 dark:hover:bg-zinc-200"
            >
              Open the app
            </Link>
            <a
              href="#how-it-works"
              className="rounded-md px-6 py-3 text-sm font-medium text-zinc-600 transition-colors hover:text-zinc-900 dark:text-zinc-300 dark:hover:text-zinc-50"
            >
              See how it works
            </a>
          </div>
        </section>

        <section className="border-y border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-900">
          <div className="mx-auto grid w-full max-w-6xl grid-cols-1 gap-8 px-6 py-12 sm:grid-cols-3">
            {CREDIBILITY.map((item) => (
              <div key={item.label}>
                <p className="text-sm font-semibold text-zinc-900 dark:text-zinc-50">
                  {item.label}
                </p>
                <p className="mt-1 text-sm text-zinc-600 dark:text-zinc-400">
                  {item.body}
                </p>
              </div>
            ))}
          </div>
        </section>

        <section
          id="how-it-works"
          className="mx-auto w-full max-w-6xl scroll-mt-16 px-6 py-20"
        >
          <h2 className="text-center text-3xl font-semibold tracking-tight">
            How it works
          </h2>
          <p className="mx-auto mt-3 max-w-2xl text-center text-zinc-600 dark:text-zinc-300">
            Three steps, running continuously while anyone is speaking.
          </p>
          <ol className="mt-12 grid grid-cols-1 gap-6 sm:grid-cols-3">
            {STEPS.map((step, index) => (
              <li
                key={step.title}
                className="rounded-xl border border-zinc-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-900"
              >
                <span className="flex h-8 w-8 items-center justify-center rounded-full bg-zinc-900 text-sm font-semibold text-white dark:bg-zinc-50 dark:text-zinc-900">
                  {index + 1}
                </span>
                <h3 className="mt-4 text-lg font-semibold tracking-tight">
                  {step.title}
                </h3>
                <p className="mt-2 text-sm text-zinc-600 dark:text-zinc-400">
                  {step.body}
                </p>
              </li>
            ))}
          </ol>
        </section>

        <section className="mx-auto w-full max-w-6xl px-6 pb-24">
          <div className="rounded-2xl border border-zinc-200 bg-zinc-50 px-6 py-14 text-center dark:border-zinc-800 dark:bg-zinc-900">
            <h2 className="mx-auto max-w-2xl text-balance text-3xl font-semibold tracking-tight">
              Bring the receipts to the next debate
            </h2>
            <p className="mx-auto mt-4 max-w-xl text-zinc-600 dark:text-zinc-300">
              Open the analyser, play a stream, and watch verdicts arrive with
              their sources.
            </p>
            <Link
              href="/login"
              className="mt-8 inline-block rounded-md bg-zinc-900 px-6 py-3 text-sm font-medium text-white transition-colors hover:bg-zinc-700 dark:bg-zinc-50 dark:text-zinc-900 dark:hover:bg-zinc-200"
            >
              Open the app
            </Link>
          </div>
        </section>
      </main>

      <footer className="mx-auto w-full max-w-6xl px-6 py-8 text-sm text-zinc-500 dark:text-zinc-400">
        Truth in Stream
      </footer>
    </div>
  );
}
