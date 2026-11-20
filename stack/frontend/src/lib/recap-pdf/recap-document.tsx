// The recap as a PDF document: the same projection and the same sheet order as
// the recap page, laid out for A4 paper. The cover and the recap table are one
// page each; each main speaker's sheet starts on a new page and, when it is
// longer than a page, continues on the next with its header repeated. Every
// page after the cover carries the brand footer and a page number.
//
// No line height is set anywhere: the typefaces' own line box already reads
// well at these sizes, and with @react-pdf/renderer 4.9.0 a line height
// inherited from the page makes per-page texts (the page number, the
// continuation header) vanish.
//
// Colours are the light theme's, fixed: paper has no dark mode. Nothing is told
// apart by colour alone - verdicts keep their text label and the distribution
// bar keeps its legend - so the report holds up printed in greyscale.
import {
  Document,
  Link,
  Page,
  Path,
  Rect,
  StyleSheet,
  Svg,
  Text,
  View,
} from "@react-pdf/renderer";
import type { AppDictionary } from "@/components/i18n/app-i18n";
import type { Locale } from "@/lib/i18n/config";
import { formatTemplate } from "@/lib/i18n/text";
import { formatTime } from "@/lib/playback/format-time";
import {
  addCounts,
  formatDuration,
  QUOTE_LIMIT,
  type Recap,
  type RecapCounts,
  type RecapSpeaker,
  REPORT_TIME_ZONE,
  truncateQuote,
  unverifiedShare,
} from "@/lib/video/recap";
import { speakerDisplayName } from "@/lib/video/speaker-display-name";
import { DISPLAY_FONT, displayFontFor, displayText, printable, TEXT_FONT } from "./fonts";
import {
  chooseSheetVariant,
  COLUMN_GAP,
  isBreakable,
  PAGE,
  type SheetBlock,
  sheetBlocks,
  splitColumns,
  VARIANTS,
} from "./layout";

const INK = "#12151d";
const MUTED = "#5b6070";
const RULE = "#d9dbe1";
const BLEU = "#0e3a6b";

// OUTCOMES is the fixed order, colour and greyscale-distinct tone of the four
// outcomes, shared by the bar and its legend.
const OUTCOMES = [
  { key: "credible", color: "#047857" },
  { key: "disputed", color: "#c8102e" },
  { key: "unverifiable", color: "#52525c" },
  { key: "notVerified", color: "#c9ccd4" },
] as const satisfies readonly { key: keyof RecapCounts; color: string }[];

const styles = StyleSheet.create({
  page: {
    paddingTop: PAGE.margin,
    paddingHorizontal: PAGE.margin,
    paddingBottom: PAGE.bottom,
    fontFamily: TEXT_FONT,
    fontSize: 10,
    color: INK,
  },
  footerRule: {
    position: "absolute",
    left: PAGE.margin,
    right: PAGE.margin,
    bottom: 44,
    borderTopWidth: 0.5,
    borderTopColor: RULE,
  },
  footerText: {
    position: "absolute",
    left: PAGE.margin,
    right: PAGE.margin,
    bottom: 28,
    fontSize: 8,
    color: MUTED,
  },
  continued: {
    position: "absolute",
    left: PAGE.margin,
    right: PAGE.margin,
    top: 18,
    fontSize: 9,
    fontWeight: 600,
  },
  brand: { flexDirection: "row", alignItems: "center", gap: 8 },
  brandName: { fontSize: 14, fontWeight: 600 },
  tricolore: { flexDirection: "row", height: 2, marginTop: 14 },
  kicker: { fontSize: 9, fontWeight: 600, color: MUTED, textTransform: "uppercase", letterSpacing: 0.6 },
  title: { fontFamily: DISPLAY_FONT, fontWeight: 600, fontSize: 30, marginTop: 6 },
  heading: { fontFamily: DISPLAY_FONT, fontWeight: 600, fontSize: 20 },
  label: { fontSize: 8, color: MUTED },
  figure: { fontFamily: DISPLAY_FONT, fontWeight: 600, fontSize: 22 },
  row: { flexDirection: "row", borderBottomWidth: 0.5, borderBottomColor: RULE, paddingVertical: 5, alignItems: "center" },
  cell: { width: 46, textAlign: "right", fontSize: 9 },
  // Column headers are set smaller than the figures: the longest French label
  // is a single word ("Invérifiables") that must fit its cell without breaking.
  headerCell: { width: 46, textAlign: "right", fontSize: 6, color: MUTED },
  nameCell: { flex: 1, fontSize: 9, fontWeight: 600, paddingRight: 6 },
  barCell: { width: 66, paddingLeft: 10 },
  bar: { flexDirection: "row", height: 7, borderWidth: 0.5, borderColor: MUTED },
  noteHeading: { fontSize: 8, fontWeight: 600, color: MUTED, textTransform: "uppercase", letterSpacing: 0.6, marginBottom: 3 },
  sectionHeading: { fontWeight: 600, textTransform: "uppercase", letterSpacing: 0.5, color: MUTED },
  statement: { borderLeftWidth: 1.5, borderLeftColor: RULE, paddingLeft: 7 },
  link: { color: BLEU, textDecoration: "underline" },
});

export type RecapDocumentProps = {
  recap: Recap;
  // The main speakers in the order of the sheets, as shown on the page.
  sheets: RecapSpeaker[];
  t: AppDictionary;
  locale: Locale;
  brandName: string;
};

// DisplayText prints a title or a name from data in the display face when
// that face can draw all of it, and in the text face otherwise.
function DisplayText({
  style,
  children,
}: {
  style: (typeof styles)[keyof typeof styles];
  children: string;
}) {
  const text = displayText(children);
  return <Text style={[style, { fontFamily: displayFontFor(text) }]}>{text}</Text>;
}

// displayName is how a voice is printed in the document: its display name,
// made printable.
function displayName(speaker: RecapSpeaker, t: AppDictionary): string {
  return printable(speakerDisplayName(speaker, t));
}

export function RecapDocument({ recap: source, sheets, t, locale, brandName }: RecapDocumentProps) {
  // The title is data: make it printable once, for every place that shows it.
  const recap = { ...source, title: printable(source.title) };
  return (
    <Document title={recap.title} author={brandName} language={locale}>
      <CoverPage recap={recap} sheets={sheets} t={t} locale={locale} brandName={brandName} />
      <Page size="A4" style={styles.page}>
        <RecapTablePage recap={recap} sheets={sheets} t={t} />
        <Footer t={t} brandName={brandName} title={recap.title} />
      </Page>
      {sheets.map((speaker) => (
        <SheetPage
          key={speaker.label}
          speaker={speaker}
          t={t}
          brandName={brandName}
          title={recap.title}
        />
      ))}
    </Document>
  );
}

// Footer is repeated on every page it is placed in: the brand, the report's
// title and the page number. It sits in the bottom band the page padding
// reserves, so content can never reach it.
function Footer({ t, brandName, title }: { t: AppDictionary; brandName: string; title: string }) {
  // Both texts span the full content width: a per-page text is laid out before
  // its content is known, so it needs both edges to get a width at all.
  return (
    <>
      <View fixed style={styles.footerRule} />
      <Text fixed style={styles.footerText}>
        {brandName} · {truncateQuote(title, 70)}
      </Text>
      <Text
        fixed
        style={[styles.footerText, { textAlign: "right" }]}
        render={({ pageNumber, totalPages }) =>
          formatTemplate(t.recap.pdf.page, { page: pageNumber, total: totalPages })
        }
      />
    </>
  );
}

// BrandMark is the app's verification mark, redrawn with PDF primitives.
function BrandMark() {
  return (
    <Svg width={26} height={26} viewBox="0 0 40 40">
      <Rect x={1} y={1} width={38} height={38} rx={12} fill="#fbfbf9" stroke={RULE} strokeWidth={1} />
      <Path d="M11 20 L17 26.5" stroke="#0055A4" strokeWidth={4} strokeLinecap="round" />
      <Path d="M18.7 25 L30 11" stroke="#EF4135" strokeWidth={4} strokeLinecap="round" />
    </Svg>
  );
}

function CoverPage({ recap, sheets, t, locale, brandName }: RecapDocumentProps) {
  const copy = t.recap.cover;
  const whole = addCounts(recap.principals.counts, recap.others.counts);
  const figures = [
    { label: copy.duration, value: formatDuration(recap.duration) },
    { label: copy.detected, value: String(whole.detected) },
    { label: copy.credible, value: String(whole.credible) },
    { label: copy.disputed, value: String(whole.disputed) },
  ];
  return (
    <Page size="A4" style={styles.page}>
      <View style={styles.brand}>
        <BrandMark />
        <Text style={styles.brandName}>{brandName}</Text>
      </View>
      <View style={styles.tricolore}>
        <View style={{ flex: 1, backgroundColor: "#0055a4" }} />
        <View style={{ flex: 1, backgroundColor: "#ffffff" }} />
        <View style={{ flex: 1, backgroundColor: "#ef4135" }} />
      </View>
      <View style={{ marginTop: 150 }}>
        <Text style={styles.kicker}>{copy.kicker}</Text>
        <DisplayText style={styles.title}>{recap.title}</DisplayText>
      </View>
      <View style={{ marginTop: 36 }}>
        <Text style={styles.kicker}>{copy.participants}</Text>
        {sheets.length === 0 ? (
          <Text style={{ marginTop: 6, color: MUTED }}>{copy.noParticipants}</Text>
        ) : (
          sheets.map((speaker) => (
            <Text key={speaker.label} style={{ marginTop: 5, fontSize: 13, fontWeight: 600 }}>
              {displayName(speaker, t)}
            </Text>
          ))
        )}
      </View>
      <View style={{ flexDirection: "row", gap: 12, marginTop: 44 }}>
        {figures.map((figure) => (
          <View key={figure.label} style={{ flex: 1, borderTopWidth: 1, borderTopColor: INK, paddingTop: 8 }}>
            <Text style={styles.figure}>{figure.value}</Text>
            <Text style={styles.label}>{figure.label}</Text>
          </View>
        ))}
      </View>
      {recap.analyzedAt ? (
        <Text style={{ marginTop: 40, fontSize: 9, color: MUTED }}>
          {formatTemplate(copy.analysedOn, {
            date: new Intl.DateTimeFormat(locale, {
              dateStyle: "long",
              timeZone: REPORT_TIME_ZONE,
            }).format(new Date(recap.analyzedAt)),
          })}
        </Text>
      ) : null}
    </Page>
  );
}

function DistributionBar({ counts }: { counts: RecapCounts }) {
  if (counts.detected === 0) {
    return <View style={styles.bar} />;
  }
  return (
    <View style={styles.bar}>
      {OUTCOMES.map(({ key, color }) =>
        counts[key] > 0 ? (
          <View key={key} style={{ flex: counts[key], backgroundColor: color }} />
        ) : null,
      )}
    </View>
  );
}

function Legend({ t }: { t: AppDictionary }) {
  return (
    <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 4, marginTop: 10 }}>
      {OUTCOMES.map(({ key, color }) => (
        <View key={key} style={{ flexDirection: "row", alignItems: "center", gap: 4, width: "49%" }}>
          <View style={{ width: 8, height: 8, backgroundColor: color, borderWidth: 0.5, borderColor: MUTED }} />
          <Text style={styles.label}>{t.recap.distribution[key]}</Text>
        </View>
      ))}
    </View>
  );
}

function CountCells({ counts }: { counts: RecapCounts }) {
  return (
    <>
      <Text style={styles.cell}>{counts.detected}</Text>
      <Text style={styles.cell}>{counts.credible}</Text>
      <Text style={styles.cell}>{counts.disputed}</Text>
      <Text style={styles.cell}>{counts.unverifiable}</Text>
      <Text style={styles.cell}>{counts.notVerified}</Text>
    </>
  );
}

function shareLabel(recap: Recap, share: number): string {
  return recap.principals.speakingTime > 0 ? `${Math.round(share * 100)} %` : "–";
}

function RecapTablePage({ recap, sheets, t }: { recap: Recap; sheets: RecapSpeaker[]; t: AppDictionary }) {
  const copy = t.recap.table;
  const notes = t.recap.notes;
  const counts = recap.principals.counts;
  const voices = recap.speakers.filter((speaker) => speaker.label !== "");
  const named = voices.filter((speaker) => speaker.name !== "");
  const confirmed = named.filter((speaker) => speaker.nameConfirmed).length;
  const others = recap.speakers.filter((speaker) => speaker.role !== "principal");
  const columns = ["speakingTime", "share", "turns", "detected", "credible", "disputed", "unverifiable", "notVerified"] as const;
  return (
    <View>
      <Text style={styles.heading}>{copy.heading}</Text>
      {sheets.length === 0 ? (
        <Text style={{ marginTop: 12, color: MUTED }}>{copy.empty}</Text>
      ) : (
        <View style={{ marginTop: 12 }}>
          <View style={[styles.row, { borderBottomColor: INK }]}>
            <Text style={[styles.nameCell, styles.label, { fontWeight: 400 }]}>{copy.speaker}</Text>
            {columns.map((column) => (
              <Text key={column} style={styles.headerCell}>
                {copy[column]}
              </Text>
            ))}
            <Text style={[styles.barCell, styles.label, { fontSize: 6 }]}>{copy.distribution}</Text>
          </View>
          {sheets.map((speaker) => (
            <View key={speaker.label} style={styles.row} wrap={false}>
              <Text style={styles.nameCell}>{displayName(speaker, t)}</Text>
              <Text style={styles.cell}>{formatDuration(speaker.speakingTime)}</Text>
              <Text style={styles.cell}>{shareLabel(recap, speaker.share)}</Text>
              <Text style={styles.cell}>{speaker.turns}</Text>
              <CountCells counts={speaker.counts} />
              <View style={styles.barCell}>
                <DistributionBar counts={speaker.counts} />
              </View>
            </View>
          ))}
          <View style={[styles.row, { borderBottomWidth: 0 }]} wrap={false}>
            <Text style={styles.nameCell}>{copy.total}</Text>
            <Text style={[styles.cell, { fontWeight: 600 }]}>{formatDuration(recap.principals.speakingTime)}</Text>
            <Text style={[styles.cell, { fontWeight: 600 }]}>{shareLabel(recap, 1)}</Text>
            <Text style={[styles.cell, { fontWeight: 600 }]}>{recap.principals.turns}</Text>
            <CountCells counts={counts} />
            <View style={styles.barCell}>
              <DistributionBar counts={counts} />
            </View>
          </View>
        </View>
      )}
      <Legend t={t} />
      <View style={{ flexDirection: "row", gap: COLUMN_GAP, marginTop: 22, fontSize: 9 }}>
        <View style={{ flex: 1 }}>
          <Text style={styles.noteHeading}>{notes.readingHeading}</Text>
          <Text>{notes.reading}</Text>
          {others.length > 0 ? (
            <View style={{ marginTop: 10 }}>
              <Text style={styles.noteHeading}>{notes.othersHeading}</Text>
              {others.map((speaker) => (
                <Text key={speaker.label}>
                  {formatTemplate(notes.other, {
                    name: displayName(speaker, t),
                    role: t.speakerNaming.roles[speaker.role],
                    time: formatDuration(speaker.speakingTime),
                    detected: speaker.counts.detected,
                  })}
                </Text>
              ))}
            </View>
          ) : null}
        </View>
        <View style={{ flex: 1 }}>
          <Text style={styles.noteHeading}>{notes.limitsHeading}</Text>
          <Text>
            {counts.detected === 0
              ? notes.noStatements
              : formatTemplate(notes.unverified, {
                  pct: Math.round(unverifiedShare(counts) * 100),
                  count: counts.unverifiable + counts.notVerified,
                  total: counts.detected,
                })}
          </Text>
          <Text style={{ marginTop: 4 }}>
            {formatTemplate(notes.attribution, {
              automatic: named.length - confirmed,
              confirmed,
              unnamed: voices.length - named.length,
            })}
          </Text>
          <Text style={{ marginTop: 4 }}>{notes.ranking}</Text>
        </View>
      </View>
    </View>
  );
}

// SheetPage is one main speaker's sheet. It is its own page sequence, so a
// sheet always starts on a new page and the continuation header - shown from
// its second page on - counts the pages of this sheet only.
function SheetPage({
  speaker,
  t,
  brandName,
  title,
}: {
  speaker: RecapSpeaker;
  t: AppDictionary;
  brandName: string;
  title: string;
}) {
  const copy = t.recap.sheet;
  const columns = t.recap.table;
  const name = displayName(speaker, t);
  const blocks = sheetBlocks(speaker, copy);
  const hasFootnote = speaker.counts.unverifiable + speaker.counts.notVerified > 0;
  const variant = chooseSheetVariant(blocks, { name, hasFootnote });
  const { fontSize } = VARIANTS[variant];
  const figures = [
    { label: columns.speakingTime, value: formatDuration(speaker.speakingTime) },
    { label: columns.share, value: `${Math.round(speaker.share * 100)} %` },
    { label: columns.turns, value: speaker.turns },
    { label: columns.detected, value: speaker.counts.detected },
    { label: columns.credible, value: speaker.counts.credible },
    { label: columns.disputed, value: speaker.counts.disputed },
    { label: columns.unverifiable, value: speaker.counts.unverifiable },
    { label: columns.notVerified, value: speaker.counts.notVerified },
  ];
  return (
    <Page size="A4" style={styles.page}>
      {/* The continuation header sits in the top margin band, which every
          page already has, so it takes no room from the first page - where it
          prints nothing - and never overlaps the content of the next ones. */}
      <Text
        fixed
        style={styles.continued}
        render={({ subPageNumber }) =>
          subPageNumber > 1 ? formatTemplate(t.recap.pdf.continued, { name }) : ""
        }
      />
      <DisplayText style={styles.heading}>{name}</DisplayText>
      {speaker.name !== "" ? (
        <Text style={styles.label}>
          {speaker.nameConfirmed ? copy.confirmedName : copy.automaticName}
        </Text>
      ) : null}
      <View style={{ flexDirection: "row", gap: 6, marginTop: 12 }}>
        {figures.map((figure) => (
          <View key={figure.label} style={{ flex: 1 }}>
            <Text style={{ fontSize: 13, fontWeight: 600 }}>{figure.value}</Text>
            <Text style={[styles.label, { fontSize: 7 }]}>{figure.label}</Text>
          </View>
        ))}
      </View>
      <View style={{ marginTop: 10, marginBottom: 14 }}>
        <DistributionBar counts={speaker.counts} />
      </View>
      <View style={{ flexDirection: "row", gap: COLUMN_GAP, fontSize }}>
        {splitColumns(blocks, variant).map((column, index) => (
          <View key={index} style={{ flex: 1 }}>
            {column.map((block) => (
              <Block
                key={block.key}
                block={block}
                t={t}
                fontSize={fontSize}
                breakable={isBreakable(block, variant)}
              />
            ))}
          </View>
        ))}
      </View>
      {hasFootnote ? (
        <Text style={[styles.label, { marginTop: 10 }]}>
          {formatTemplate(copy.uncounted, {
            unverifiable: speaker.counts.unverifiable,
            notVerified: speaker.counts.notVerified,
          })}
        </Text>
      ) : null}
      <Footer t={t} brandName={brandName} title={title} />
    </Page>
  );
}

// sourceLabel is what a source link prints: its title, or its host when the
// source has none and the "title" is the bare URL, which is too long to read
// and to fit a column.
function sourceLabel(source: { title: string; url: string }): string {
  if (source.title !== source.url) {
    return printable(source.title);
  }
  try {
    return new URL(source.url).host;
  } catch {
    return source.title;
  }
}

function Block({
  block,
  t,
  fontSize,
  breakable,
}: {
  block: SheetBlock;
  t: AppDictionary;
  fontSize: number;
  breakable: boolean;
}) {
  if (block.kind === "heading") {
    // minPresenceAhead keeps a heading off the bottom of a page, away from the
    // statements it introduces.
    return (
      <Text style={[styles.sectionHeading, { fontSize: fontSize - 1, marginBottom: 5 }]} minPresenceAhead={40}>
        {block.text}
      </Text>
    );
  }
  if (block.kind === "empty") {
    return <Text style={{ color: MUTED, marginBottom: fontSize }}>{block.text}</Text>;
  }
  const { statement } = block;
  const copy = t.recap.sheet;
  const verdicts: Record<string, string> = t.claims.verdicts;
  const flags: Record<string, string> = t.claims.flags;
  const meta = [
    formatTime(statement.at),
    verdicts[statement.verdict] ?? statement.verdict,
    ...statement.flags.map((flag) => flags[flag] ?? flag),
  ].join("  ·  ");
  return (
    // A statement is kept whole on its page, unless it is so long that it
    // could not fit one: then it may continue on the next.
    <View wrap={breakable} style={[styles.statement, { marginBottom: fontSize }]}>
      <Text style={{ fontSize: fontSize - 1, fontWeight: 600 }}>{meta}</Text>
      <Text style={{ fontStyle: "italic" }}>
        {printable(truncateQuote(statement.quote, QUOTE_LIMIT))}
      </Text>
      {statement.rationale !== "" ? (
        <Text>
          <Text style={{ fontWeight: 600 }}>{copy.justification} : </Text>
          {printable(statement.rationale)}
        </Text>
      ) : null}
      {statement.sources.length === 0 ? (
        <Text style={{ color: MUTED, fontSize: fontSize - 1 }}>{copy.noSource}</Text>
      ) : (
        <Text style={{ fontSize: fontSize - 1 }}>
          {statement.sources.map((source, index) => (
            <Text key={source.url}>
              {index > 0 ? "   " : ""}
              <Link src={source.url} style={styles.link}>
                {sourceLabel(source)}
              </Link>
            </Text>
          ))}
        </Text>
      )}
    </View>
  );
}
