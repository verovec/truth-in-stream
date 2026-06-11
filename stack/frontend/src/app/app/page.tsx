import { LibraryExperience } from "./_components/library-experience";
import { LogoutButton } from "./_components/logout-button";

export default function Home() {
  return (
    <div className="flex flex-1 flex-col bg-zinc-50 dark:bg-zinc-900">
      <header className="flex items-center justify-between border-b border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-950 sm:px-6">
        <h1 className="text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">
          Truth in Stream
        </h1>
        <LogoutButton />
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 p-4 sm:p-6">
        <LibraryExperience />
      </main>
    </div>
  );
}
