import type { AppDictionary } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import type { RecapSpeaker } from "@/lib/video/recap";

// speakerDisplayName is how the report prints a voice: its resolved name, or
// its diarized letter when it has none. The report never invents a name.
// Speech the transcriber attributed to nobody has no letter either.
export function speakerDisplayName(
  speaker: Pick<RecapSpeaker, "label" | "name">,
  t: AppDictionary,
): string {
  if (speaker.name !== "") {
    return speaker.name;
  }
  return speaker.label === ""
    ? t.recap.unattributed
    : formatTemplate(t.recap.speakerFallback, { label: speaker.label });
}
