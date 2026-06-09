import { PlaybackProvider } from "@/components/playback/playback-provider";
import { VideoPlayer } from "@/components/playback/video-player";
import { FactCheckPanel } from "./_components/fact-check-panel";

const SAMPLE_VIDEO = {
  src: "https://media.w3.org/2010/05/sintel/trailer.mp4",
  title: "Sample video",
};

export default function Home() {
  return (
    <div className="flex flex-1 flex-col bg-zinc-50 dark:bg-zinc-900">
      <header className="border-b border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-950 sm:px-6">
        <h1 className="text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">
          Truth in Stream
        </h1>
      </header>
      <main className="mx-auto grid w-full max-w-6xl flex-1 grid-cols-1 items-start gap-4 p-4 sm:p-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <PlaybackProvider>
          <VideoPlayer src={SAMPLE_VIDEO.src} title={SAMPLE_VIDEO.title} />
          <FactCheckPanel source={SAMPLE_VIDEO.src} />
        </PlaybackProvider>
      </main>
    </div>
  );
}
