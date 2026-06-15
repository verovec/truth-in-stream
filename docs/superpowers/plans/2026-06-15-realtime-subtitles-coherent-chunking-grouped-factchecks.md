# Real-time subtitles, coherent chunking, and grouped fact-checks — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make live subtitles stream in real time, cut analysis chunks at coherent sentence boundaries, and group fact-check results by the matched enwiki claim/source into a single scored card under the subtitles.

**Architecture:** Two surgical backend changes (raise the AssemblyAI turn-silence threshold so the live caption builds a whole utterance; make the analyzer's idle flush sentence-aware so a unit is never cut mid-sentence) plus a new frontend derivation that groups checked statements by their top matched source/claim and renders them as merged cards with one aggregated corroboration score. No schema changes, no new services.

**Tech Stack:** Go (stdlib `net/http`, `testing/synctest`), Next.js 16 / React 19 / TypeScript, Vitest, Testing Library.

**Spec:** `docs/superpowers/specs/2026-06-15-realtime-subtitles-coherent-chunking-grouped-factchecks-design.md`

## Repo conventions (read before starting)

- Backend tests need the local Go toolchain (`~/sdk/go1.26.4`); the system `go` (1.20) cannot build this repo and `testing/synctest` requires Go 1.25+. Every Go command below is prefixed with `PATH="$HOME/sdk/go1.26.4/bin:$PATH"`.
- Run all frontend commands from `stack/frontend` (a prior `cd` into a worktree root breaks bare `npx vitest`; always `cd stack/frontend` first).
- Layering: live flush logic stays in `internal/service`; provider config stays in `internal/transcribe`. Wrap errors with `%w`. `gofmt`/`go vet`/`golangci-lint` clean; ESLint clean.
- This repo is indexed by GitNexus; its index is currently stale. The targeted unit tests in each task are the authoritative safety check here — you do not need to refresh GitNexus to execute this plan.

---

## Task 1: Sentence-aware idle flush (backend)

Make the analyzer's idle timer refuse to cut a unit whose buffered text ends mid-sentence, holding it for a bounded number of extra idle windows so the thought can finish, while a never-completing sentence still flushes (no hang).

**Files:**
- Modify: `stack/backend/internal/service/live.go` (add helpers + the `<-timer.C` logic in `analyzeLoop` ~lines 344-396; refactor `sentenceCount` ~line 569)
- Test: `stack/backend/internal/service/live_test.go`

- [ ] **Step 1: Write the failing unit test for the boundary helper**

Add to `stack/backend/internal/service/live_test.go`:

```go
func TestEndsAtSentenceBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"period", "all done.", true},
		{"question", "is it?", true},
		{"exclaim", "wow!", true},
		{"ellipsis", "well…", true},
		{"trailing spaces after period", "done.  ", true},
		{"mid sentence", "the quick brown fox", false},
		{"comma is not terminal", "first, then", false},
		{"empty", "", false},
		{"spaces only", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := endsAtSentenceBoundary(tc.text); got != tc.want {
				t.Errorf("endsAtSentenceBoundary(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to confirm it fails to compile**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test ./internal/service/ -run TestEndsAtSentenceBoundary
```
Expected: FAIL — `undefined: endsAtSentenceBoundary`.

- [ ] **Step 3: Add the helpers and refactor `sentenceCount`**

In `stack/backend/internal/service/live.go`, add `"unicode/utf8"` to the import block (it already imports `strings` and `unicode`).

Add these helpers just above `sentenceCount` (which starts ~line 569):

```go
// isSentenceTerminator reports whether r ends a sentence. It is the single
// definition of sentence-terminating punctuation, shared by sentenceCount (which
// counts boundaries) and endsAtSentenceBoundary (which checks the final one).
func isSentenceTerminator(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '…'
}

// endsAtSentenceBoundary reports whether text's last non-space rune ends a
// sentence. The idle flush uses it so a buffered unit is never scored
// mid-sentence: a buffer that ends inside a sentence is held for a bounded grace
// window instead of being cut into an incoherent fragment.
func endsAtSentenceBoundary(text string) bool {
	trimmed := strings.TrimRightFunc(text, unicode.IsSpace)
	if trimmed == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(trimmed)
	return isSentenceTerminator(r)
}
```

In `sentenceCount`, replace the terminal-punctuation line:

```go
		term := r == '.' || r == '!' || r == '?' || r == '…'
```
with:
```go
		term := isSentenceTerminator(r)
```

- [ ] **Step 4: Run the helper test and the existing sentenceCount-dependent tests**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test ./internal/service/ -run 'TestEndsAtSentenceBoundary|TestLiveAnalyzerSplitsSameSpeakerRunAtSentenceCap'
```
Expected: PASS (the `sentenceCount` refactor is behavior-preserving).

- [ ] **Step 5: Add a `liveUnit` method and the `maxIdleExtensions` constant**

In `stack/backend/internal/service/live.go`, add this method just after `func (u *liveUnit) take()` (~line 273):

```go
// endsAtSentenceBoundary reports whether the unit's most recent segment ends a
// sentence, so the idle flush can hold a mid-sentence buffer for its grace
// window rather than cutting a fragment.
func (u *liveUnit) endsAtSentenceBoundary() bool {
	if len(u.members) == 0 {
		return false
	}
	return endsAtSentenceBoundary(u.members[len(u.members)-1].seg.Text)
}
```

Add this constant next to `defaultIdleFlush` (~line 88):

```go
// maxIdleExtensions bounds how many extra idle windows a mid-sentence buffer is
// held before it is scored anyway, so a sentence that never completes still
// flushes after at most maxIdleExtensions+1 idle windows instead of hanging.
const maxIdleExtensions = 2
```

- [ ] **Step 6: Write the failing behavioral test (grace window lets a sentence finish)**

Add to `stack/backend/internal/service/live_test.go`. First add this scripted stream helper near the other fakes (after `pausingStream`, ~line 72):

```go
// scriptedStream emits each transcript after waiting its delay, so a test can
// place a pause between turns on the fake clock and observe the idle timer fire
// between them. It holds the channel open after the script so the analyzer only
// stops on ctx cancellation.
type scriptedStream struct {
	steps []scriptStep
}

type scriptStep struct {
	delay time.Duration
	tr    domain.LiveTranscript
}

func (s scriptedStream) StreamSegments(ctx context.Context, _ <-chan []byte) (<-chan domain.LiveTranscript, error) {
	ch := make(chan domain.LiveTranscript)
	go func() {
		defer close(ch)
		for _, st := range s.steps {
			if st.delay > 0 {
				timer := time.NewTimer(st.delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
			select {
			case ch <- st.tr:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return ch, nil
}
```

Then add the test:

```go
func TestLiveAnalyzerHoldsMidSentenceBufferAcrossIdle(t *testing.T) {
	// An idle window that elapses while the buffer ends mid-sentence must not cut
	// the unit: the next same-speaker turn completes the sentence and the two
	// score together as one coherent claim. synctest drives the idle timer on a
	// fake clock so the pause between turns is deterministic.
	synctest.Test(t, func(t *testing.T) {
		first := domain.Segment{Start: 0, End: time.Second, Text: "the quick brown fox", Speaker: "A"}
		second := domain.Segment{Start: 3 * time.Second, End: 4 * time.Second, Text: "jumps over the lazy dog.", Speaker: "A"}
		mc := &countingMatcher{}
		analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
			Stream: scriptedStream{steps: []scriptStep{
				{delay: 0, tr: domain.LiveTranscript{Segment: first, Final: true}},
				// Send the completer 0.5s after the 2s idle window has fired once,
				// so the buffer is mid-sentence when the timer elapses.
				{delay: 2*time.Second + 500*time.Millisecond, tr: domain.LiveTranscript{Segment: second, Final: true}},
			}},
			Matcher:    mc,
			Prechecker: livePrechecker{},
			Logger:     discardLogger(),
			IdleFlush:  2 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewLiveAnalyzer: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		out, err := analyzer.Run(ctx, make(chan []byte))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		events := drainLiveEvents(t, out)
		_ = events
		wantCalls := []string{"the quick brown fox jumps over the lazy dog."}
		if diff := cmp.Diff(wantCalls, mc.calls()); diff != "" {
			t.Errorf("mid-sentence buffer should join the completing turn (-want +got):\n%s", diff)
		}
	})
}
```

- [ ] **Step 7: Run it to confirm it fails with the current cut-mid-sentence behavior**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test ./internal/service/ -run TestLiveAnalyzerHoldsMidSentenceBufferAcrossIdle
```
Expected: FAIL — current code idle-flushes `"the quick brown fox"` alone at t=2s, so `mc.calls()` is `["the quick brown fox", "jumps over the lazy dog."]`, not the single combined call.

- [ ] **Step 8: Implement the sentence-aware idle flush in `analyzeLoop`**

In `stack/backend/internal/service/live.go`, in `analyzeLoop`, add an extension counter beside `seq := 0` (~line 331):

```go
	var unit liveUnit
	seq := 0
	idleExtensions := 0
```

Replace the `<-timer.C` case (~lines 348-353):

```go
		case <-timer.C:
			// An idle buffer is scored now rather than held for speech that may
			// never come, so a trailing short turn still gets a verdict.
			if !flush() {
				return
			}
```
with:
```go
		case <-timer.C:
			// Idle elapsed. Avoid cutting mid-sentence: a buffer that does not yet
			// end on a sentence boundary is held for a bounded number of extra idle
			// windows so the thought can finish. Past that bound it is scored anyway,
			// so a sentence that never completes cannot hang the unit.
			if !unit.empty() && !unit.endsAtSentenceBoundary() && idleExtensions < maxIdleExtensions {
				idleExtensions++
				timer.Reset(a.idleFlush)
				continue
			}
			idleExtensions = 0
			if !flush() {
				return
			}
```

In the finalized-transcript branch, reset the counter when a new segment joins. After `unit.add(id, seg, sentences)` (~line 384), add the reset:

```go
			unit.add(id, seg, sentences)
			idleExtensions = 0
			timer.Stop()
			timer.Reset(a.idleFlush)
```

- [ ] **Step 9: Run the new test and the existing idle/boundary tests together**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test -race ./internal/service/ -run 'TestLiveAnalyzerHoldsMidSentenceBufferAcrossIdle|TestLiveAnalyzerIdleFlushesTrailingShortTurn|TestLiveAnalyzerSplitsSameSpeakerRunAtSentenceCap|TestLiveAnalyzerFlushesPriorSpeakerBeforeNewSpeaker'
```
Expected: PASS (the existing idle test uses `"a lone remark."`, which ends at a boundary, so it still flushes after one window).

- [ ] **Step 10: Format, vet, and run the full service package with the race detector**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" gofmt -w . && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go vet ./... && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test -race ./internal/service/
```
Expected: PASS, no vet or format output.

- [ ] **Step 11: Commit**

```bash
git add stack/backend/internal/service/live.go stack/backend/internal/service/live_test.go
git commit -m "feat(live): hold mid-sentence buffers across idle so chunks cut at sentence boundaries"
```

---

## Task 2: Let the live caption span a whole utterance (backend)

Raise the AssemblyAI end-of-turn silence threshold so an in-progress turn keeps streaming partials (the live caption) until a natural pause, instead of committing every ~500 ms — the root cause of "I only see the beginning of the section."

**Files:**
- Modify: `stack/backend/internal/transcribe/assemblyai.go:52-59` (constant + comment)
- Test: `stack/backend/internal/transcribe/assemblyai_test.go:46`

- [ ] **Step 1: Update the failing test to expect the new threshold**

In `stack/backend/internal/transcribe/assemblyai_test.go`, in `TestAssemblyAIURL`, change the expected value (~line 46):

```go
		"max_turn_silence": "500",
```
to:
```go
		"max_turn_silence": "1500",
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test ./internal/transcribe/ -run TestAssemblyAIURL
```
Expected: FAIL — `query "max_turn_silence" = "500", want "1500"`.

- [ ] **Step 3: Raise the constant and rewrite its comment**

In `stack/backend/internal/transcribe/assemblyai.go`, replace the constant block (~lines 53-59):

```go
	// assemblyAIMaxTurnSilenceMs shortens end-of-turn detection from the provider's
	// 1000 ms default so a speaker's pause commits a turn sooner. Subtitles then
	// land promptly and stay short rather than arriving as one multi-second block,
	// and because a speaker change carries a pause, a turn ends at it - keeping each
	// committed segment within one speaker. It sits well above a mid-sentence
	// micro-pause, so it shortens turns without fragmenting a single sentence.
	assemblyAIMaxTurnSilenceMs = 500
```
with:
```go
	// assemblyAIMaxTurnSilenceMs sets end-of-turn detection above the provider's
	// 1000 ms default so an in-progress turn keeps streaming partials (the live
	// caption) until a genuine sentence-ending pause, instead of committing on a
	// mid-sentence micro-pause. The caption then builds a whole utterance word by
	// word rather than flashing only its first few words before the turn commits.
	// A speaker change still carries a pause long enough to end a turn, so a
	// committed segment stays within one speaker; the live analyzer's
	// sentence-aware idle flush keeps the scored unit coherent regardless.
	assemblyAIMaxTurnSilenceMs = 1500
```

- [ ] **Step 4: Run the transcribe package tests**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test -race ./internal/transcribe/
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add stack/backend/internal/transcribe/assemblyai.go stack/backend/internal/transcribe/assemblyai_test.go
git commit -m "feat(live): raise turn-silence so the live caption streams a whole utterance"
```

---

## Task 3: Group fact-checks by matched claim/source (frontend derive)

Add a pure derivation that clusters checked statements sharing their top matched source/claim into one group and aggregates a single corroboration score from the members' raw weights.

**Files:**
- Create: `stack/frontend/src/lib/live/fact-check-groups.ts`
- Test: `stack/frontend/src/lib/live/fact-check-groups.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `stack/frontend/src/lib/live/fact-check-groups.test.ts`:

```ts
import { describe, expect, test } from "vitest";
import type { Confidence, SegmentMatch } from "@/lib/fact-check/api";
import type { LiveStatement } from "./statements";
import { deriveFactCheckGroups } from "./fact-check-groups";

const checked = (
  id: string,
  start: number,
  text: string,
  overrides: Partial<Extract<LiveStatement, { status: "checked" }>> = {},
): LiveStatement => ({
  id,
  start,
  end: start + 2,
  text,
  status: "checked",
  matches: [],
  ...overrides,
});

const claim = (claimText: string, url: string): SegmentMatch => ({
  kind: "claim",
  claim: claimText,
  verdict: "corroborates",
  sources: [{ title: "Wikipedia", url }],
  similarity: 0.9,
});

const conf = (supporting: number, contradicting: number): Confidence => ({
  score: supporting / (supporting + contradicting),
  supporting,
  contradicting,
  evidenceItems: 1,
});

describe("deriveFactCheckGroups", () => {
  test("clusters statements with the same matched source and aggregates the score", () => {
    const url = "https://en.wikipedia.org/wiki/Apollo_11";
    const groups = deriveFactCheckGroups([
      checked("s1", 5, "the moon landing happened", {
        matches: [claim("Apollo 11 landed in 1969", url)],
        confidence: conf(0.9, 0.1),
      }),
      checked("s2", 40, "we landed on the moon", {
        matches: [claim("Apollo 11 landed in 1969", url)],
        confidence: conf(0.6, 0.4),
      }),
    ]);

    expect(groups).toHaveLength(1);
    expect(groups[0].members.map((m) => m.statementId)).toEqual(["s1", "s2"]);
    // supporting 1.5 / (1.5 + 0.5) = 0.75
    expect(groups[0].score).toBeCloseTo(0.75);
    expect(groups[0].match).toMatchObject({ kind: "claim" });
  });

  test("keeps statements with different sources in separate groups, in first-member order", () => {
    const groups = deriveFactCheckGroups([
      checked("s1", 5, "a", {
        matches: [claim("c-a", "https://en.wikipedia.org/wiki/A")],
        confidence: conf(1, 0),
      }),
      checked("s2", 10, "b", {
        matches: [claim("c-b", "https://en.wikipedia.org/wiki/B")],
        confidence: conf(1, 0),
      }),
    ]);
    expect(groups.map((g) => g.members[0].statementId)).toEqual(["s1", "s2"]);
  });

  test("groups evidence matches by article url", () => {
    const evidence = (excerpt: string): SegmentMatch => ({
      kind: "evidence",
      excerpt,
      article: { title: "Earth", url: "https://en.wikipedia.org/wiki/Earth" },
      similarity: 0.8,
    });
    const groups = deriveFactCheckGroups([
      checked("s1", 0, "earth one", { matches: [evidence("a")], confidence: conf(0.8, 0) }),
      checked("s2", 5, "earth two", { matches: [evidence("b")], confidence: conf(0.7, 0) }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].members).toHaveLength(2);
  });

  test("falls back to the claim text when a claim carries no source", () => {
    const noSource: SegmentMatch = {
      kind: "claim",
      claim: "uncited fact",
      verdict: "unclear",
      sources: [],
      similarity: 0.5,
    };
    const groups = deriveFactCheckGroups([
      checked("s1", 0, "x", { matches: [noSource], confidence: conf(0.5, 0) }),
      checked("s2", 5, "y", { matches: [noSource], confidence: conf(0.5, 0) }),
    ]);
    expect(groups).toHaveLength(1);
  });

  test("ignores analysing, skipped, errored, and no-match statements", () => {
    const groups = deriveFactCheckGroups([
      checked("e1", 2, "broke", {
        error: "failed",
        matches: [claim("x", "https://en.wikipedia.org/wiki/X")],
      }),
      checked("k1", 4, "small talk", {
        skipReason: "not_a_claim",
        matches: [claim("x", "https://en.wikipedia.org/wiki/X")],
      }),
      checked("n1", 6, "no hit", { matches: [] }),
      { id: "a1", start: 0, end: 1, text: "analysing", status: "analysing" },
    ]);
    expect(groups).toEqual([]);
  });
});
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
cd stack/frontend && npx vitest run src/lib/live/fact-check-groups.test.ts
```
Expected: FAIL — cannot resolve `./fact-check-groups`.

- [ ] **Step 3: Implement the derivation**

Create `stack/frontend/src/lib/live/fact-check-groups.ts`:

```ts
// Groups the fact-check results shown below the subtitles by the matched enwiki
// claim/source, so statements about the same fact - even across different
// subtitle sections - collapse into one card with a single aggregated score.
// Derived from the same live statement state that drives the transcript, so the
// two can never disagree.
import type { SegmentMatch } from "@/lib/fact-check/api";
import { isScored, type LiveStatement } from "./statements";

// FactCheckGroupMember is one statement contributing to a group, kept light: the
// id selects/scrolls its subtitle, start/snippet keep the link to the transcript
// legible.
export type FactCheckGroupMember = {
  statementId: string;
  start: number;
  snippet: string;
};

// FactCheckGroup is one matched fact: the shared top match (rendered as the
// card's claim/source header), the corroboration score aggregated across all
// member statements, and the members themselves in first-seen (start) order.
export type FactCheckGroup = {
  key: string;
  match: SegmentMatch;
  score: number;
  members: FactCheckGroupMember[];
};

// groupKey is the stable identity of a match's fact: an evidence match keys on
// its article url, a claim match on its first cited source url, falling back to
// the claim text when a curated claim carries no source. The kind-prefixed
// namespace keeps a claim and an article that happen to share a url distinct.
function groupKey(match: SegmentMatch): string {
  if (match.kind === "evidence") {
    return `article:${match.article.url}`;
  }
  const url = match.sources[0]?.url;
  return url ? `source:${url}` : `claim:${match.claim}`;
}

// deriveFactCheckGroups clusters scored statements by their top match's fact and
// aggregates each group's score from the members' raw supporting/contradicting
// weights (score = supporting / (supporting + contradicting), 0 when neither),
// mirroring the backend's confidence math so a group score reflects the whole
// group's corroboration against the enwiki dataset. Analysing, skipped, errored,
// and no-match statements contribute nothing. Map insertion order yields groups
// in first-member start order, since statements arrive start-ordered.
export function deriveFactCheckGroups(
  statements: readonly LiveStatement[],
): FactCheckGroup[] {
  type Acc = {
    group: FactCheckGroup;
    supporting: number;
    contradicting: number;
  };
  const byKey = new Map<string, Acc>();

  for (const statement of statements) {
    if (!isScored(statement) || statement.matches.length === 0) {
      continue;
    }
    const top = statement.matches[0];
    const key = groupKey(top);
    const member: FactCheckGroupMember = {
      statementId: statement.id,
      start: statement.start,
      snippet: statement.text,
    };
    const supporting = statement.confidence?.supporting ?? 0;
    const contradicting = statement.confidence?.contradicting ?? 0;

    const acc = byKey.get(key);
    if (acc) {
      acc.group.members.push(member);
      acc.supporting += supporting;
      acc.contradicting += contradicting;
    } else {
      byKey.set(key, {
        group: { key, match: top, score: 0, members: [member] },
        supporting,
        contradicting,
      });
    }
  }

  const groups: FactCheckGroup[] = [];
  for (const { group, supporting, contradicting } of byKey.values()) {
    const total = supporting + contradicting;
    group.score = total > 0 ? supporting / total : 0;
    groups.push(group);
  }
  return groups;
}
```

- [ ] **Step 4: Run the tests**

```bash
cd stack/frontend && npx vitest run src/lib/live/fact-check-groups.test.ts
```
Expected: PASS (all five tests).

- [ ] **Step 5: Commit**

```bash
git add stack/frontend/src/lib/live/fact-check-groups.ts stack/frontend/src/lib/live/fact-check-groups.test.ts
git commit -m "feat(live): derive grouped fact-checks by matched enwiki claim/source"
```

---

## Task 4: Grouped fact-check card component (frontend)

Render each group as a merged card: the member count and aggregated score, the shared match (via the existing `MatchRow`), and one clickable row per contributing statement that hands its id up for cross-highlighting.

**Files:**
- Create: `stack/frontend/src/app/app/_components/live-fact-check-groups.tsx`
- Test: `stack/frontend/src/app/app/_components/live-fact-check-groups.test.tsx`
- Reference (do not modify): `stack/frontend/src/app/app/_components/match-row.tsx`, `stack/frontend/src/app/app/_components/live-row-classes.ts`, `stack/frontend/src/lib/playback/format-time.ts`

- [ ] **Step 1: Write the failing component test**

Create `stack/frontend/src/app/app/_components/live-fact-check-groups.test.tsx`:

```tsx
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import type { FactCheckGroup } from "@/lib/live/fact-check-groups";
import { LiveFactCheckGroups } from "./live-fact-check-groups";

const group: FactCheckGroup = {
  key: "source:https://en.wikipedia.org/wiki/Apollo_11",
  match: {
    kind: "claim",
    claim: "Apollo 11 landed in 1969",
    verdict: "corroborates",
    sources: [{ title: "Wikipedia", url: "https://en.wikipedia.org/wiki/Apollo_11" }],
    similarity: 0.9,
  },
  score: 0.75,
  members: [
    { statementId: "s1", start: 5, snippet: "the moon landing happened" },
    { statementId: "s2", start: 40, snippet: "we landed on the moon" },
  ],
};

describe("LiveFactCheckGroups", () => {
  test("shows an empty hint when there are no groups", () => {
    render(
      <LiveFactCheckGroups groups={[]} selectedStatementId={null} onSelect={vi.fn()} />,
    );
    expect(screen.getByText(/fact-checks appear here as claims are verified/i)).toBeInTheDocument();
  });

  test("renders one card with the shared claim, aggregated score, and a row per member", () => {
    render(
      <LiveFactCheckGroups groups={[group]} selectedStatementId={null} onSelect={vi.fn()} />,
    );
    expect(screen.getByText(/apollo 11 landed in 1969/i)).toBeInTheDocument();
    expect(screen.getByText(/75% corroborated/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /the moon landing happened/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /we landed on the moon/i })).toBeInTheDocument();
  });

  test("hands the clicked member's statement id to onSelect", async () => {
    const onSelect = vi.fn();
    render(
      <LiveFactCheckGroups groups={[group]} selectedStatementId={null} onSelect={onSelect} />,
    );
    await userEvent.click(screen.getByRole("button", { name: /we landed on the moon/i }));
    expect(onSelect).toHaveBeenCalledWith("s2");
  });

  test("marks the card current when a member is the selected statement", () => {
    render(
      <LiveFactCheckGroups groups={[group]} selectedStatementId="s2" onSelect={vi.fn()} />,
    );
    expect(screen.getByRole("listitem", { current: true })).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
cd stack/frontend && npx vitest run src/app/app/_components/live-fact-check-groups.test.tsx
```
Expected: FAIL — cannot resolve `./live-fact-check-groups`.

- [ ] **Step 3: Implement the component**

Create `stack/frontend/src/app/app/_components/live-fact-check-groups.tsx`:

```tsx
"use client";

import { memo } from "react";
import type { FactCheckGroup } from "@/lib/live/fact-check-groups";
import { formatTime } from "@/lib/playback/format-time";
import {
  LIVE_ROW_BASE_CLASS,
  LIVE_ROW_EMPHASIZED_CLASS,
} from "./live-row-classes";
import { MatchRow } from "./match-row";

// LiveFactCheckGroups is the fact-check region below the subtitles: one merged
// card per matched fact. Each card shows how many statements resolved to that
// fact and their aggregated corroboration score, the shared match, and a row per
// contributing statement that hands its id up so the subtitle region can
// highlight and scroll the origin into view. A card is current when any of its
// members is the selected statement. Memoized so an interim caption update of the
// parent panel does not re-render it.
export const LiveFactCheckGroups = memo(function LiveFactCheckGroups({
  groups,
  selectedStatementId,
  onSelect,
}: {
  groups: FactCheckGroup[];
  selectedStatementId: string | null;
  onSelect: (statementId: string) => void;
}) {
  if (groups.length === 0) {
    return (
      <p className="text-sm text-zinc-600 dark:text-zinc-400">
        Fact-checks appear here as claims are verified.
      </p>
    );
  }

  return (
    <ol
      aria-label="Fact-check results"
      className="-mr-2 flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pr-2"
    >
      {groups.map((group) => {
        const selected = group.members.some(
          (member) => member.statementId === selectedStatementId,
        );
        return (
          <li
            key={group.key}
            aria-current={selected ? "true" : undefined}
            className={`rounded-lg border transition-colors ${
              selected ? LIVE_ROW_EMPHASIZED_CLASS : LIVE_ROW_BASE_CLASS
            }`}
          >
            <div className="flex items-baseline justify-between gap-2 px-3 py-1.5">
              <span className="text-[11px] font-semibold uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
                {group.members.length === 1
                  ? "1 statement"
                  : `${group.members.length} statements`}
              </span>
              <span className="font-mono text-[11px] tabular-nums text-zinc-500 dark:text-zinc-400">
                {Math.round(group.score * 100)}% corroborated
              </span>
            </div>
            <div className="border-t border-zinc-200 px-3 py-2 dark:border-zinc-800">
              <MatchRow match={group.match} />
            </div>
            <ul className="border-t border-zinc-200 dark:border-zinc-800">
              {group.members.map((member) => (
                <li key={member.statementId}>
                  <button
                    type="button"
                    onClick={() => onSelect(member.statementId)}
                    className="flex w-full items-baseline gap-2 px-3 py-1.5 text-left hover:bg-zinc-900/5 focus-visible:outline-2 focus-visible:outline-sky-500 dark:hover:bg-white/5"
                  >
                    <span className="font-mono text-[11px] tabular-nums text-zinc-500 dark:text-zinc-400">
                      {formatTime(member.start)}
                    </span>
                    <span className="line-clamp-1 text-xs italic text-zinc-500 dark:text-zinc-400">
                      {member.snippet}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </li>
        );
      })}
    </ol>
  );
});
```

- [ ] **Step 4: Run the component tests**

```bash
cd stack/frontend && npx vitest run src/app/app/_components/live-fact-check-groups.test.tsx
```
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add stack/frontend/src/app/app/_components/live-fact-check-groups.tsx stack/frontend/src/app/app/_components/live-fact-check-groups.test.tsx
git commit -m "feat(live): grouped fact-check card with aggregated enwiki score"
```

---

## Task 5: Wire the panel to grouped cards and remove the dead flat list (frontend)

Switch the panel from the flat per-match list to grouped cards, then delete the now-unused flat-list derivation and component so no dead code remains.

**Files:**
- Modify: `stack/frontend/src/app/app/_components/live-fact-check-panel.tsx`
- Delete: `stack/frontend/src/lib/live/fact-checks.ts`, `stack/frontend/src/lib/live/fact-checks.test.ts`, `stack/frontend/src/app/app/_components/live-fact-check-list.tsx`, `stack/frontend/src/app/app/_components/live-fact-check-list.test.tsx`
- Test (already exists, must keep passing): `stack/frontend/src/app/app/_components/live-fact-check-panel.test.tsx`

- [ ] **Step 1: Confirm the flat-list symbols are used only by the panel**

```bash
cd stack/frontend && grep -rn "deriveFactChecks\|LiveFactCheckList\|live-fact-check-list\|live/fact-checks" src/ --include=*.ts --include=*.tsx | grep -v "fact-checks.test\|live-fact-check-list"
```
Expected: the only non-test references are in `live-fact-check-panel.tsx`. If anything else references them, stop and reconcile before deleting.

- [ ] **Step 2: Repoint the panel to the grouped derivation and component**

In `stack/frontend/src/app/app/_components/live-fact-check-panel.tsx`:

Replace the import (~line 9):
```ts
import { deriveFactChecks } from "@/lib/live/fact-checks";
```
with:
```ts
import { deriveFactCheckGroups } from "@/lib/live/fact-check-groups";
```

Replace the import (~line 12):
```ts
import { LiveFactCheckList } from "./live-fact-check-list";
```
with:
```ts
import { LiveFactCheckGroups } from "./live-fact-check-groups";
```

Replace the memo (~line 57):
```ts
  const entries = useMemo(() => deriveFactChecks(statements), [statements]);
```
with:
```ts
  const groups = useMemo(() => deriveFactCheckGroups(statements), [statements]);
```

Replace the render (~lines 116-120):
```tsx
          <LiveFactCheckList
            entries={entries}
            selectedStatementId={selection?.id ?? null}
            onSelect={selectFactCheck}
          />
```
with:
```tsx
          <LiveFactCheckGroups
            groups={groups}
            selectedStatementId={selection?.id ?? null}
            onSelect={selectFactCheck}
          />
```

- [ ] **Step 3: Delete the dead flat-list files**

```bash
cd stack/frontend && git rm src/lib/live/fact-checks.ts src/lib/live/fact-checks.test.ts src/app/app/_components/live-fact-check-list.tsx src/app/app/_components/live-fact-check-list.test.tsx
```

- [ ] **Step 4: Run the panel tests and the full live test set**

```bash
cd stack/frontend && npx vitest run src/app/app/_components/live-fact-check-panel.test.tsx src/lib/live/ src/app/app/_components/live-fact-check-groups.test.tsx
```
Expected: PASS. The existing panel tests still pass because the grouped card renders the claim text (`apollo 11 landed in 1969`) and a member button named by the statement snippet (`the moon landing happened`), preserving the assertions in `live-fact-check-panel.test.tsx`.

- [ ] **Step 5: Type-check and lint the frontend**

```bash
cd stack/frontend && npx tsc --noEmit && npm run lint
```
Expected: no type errors, no lint errors. (`tsc` will flag any lingering import of the deleted modules.)

- [ ] **Step 6: Commit**

```bash
git add stack/frontend/src/app/app/_components/live-fact-check-panel.tsx
git commit -m "feat(live): render grouped fact-check cards in the panel; remove flat list"
```

---

## Task 6: Full verification and end-to-end check

Confirm the whole feature works together, not just the unit slices.

- [ ] **Step 1: Run the full backend suite with the race detector**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go test -race ./...
```
Expected: PASS (store integration tests skip without `TEST_DATABASE_URL` — do not set it to the dev DB; those tests drop tables).

- [ ] **Step 2: Lint the backend**

```bash
cd stack/backend && PATH="$HOME/sdk/go1.26.4/bin:$PATH" gofmt -l . && PATH="$HOME/sdk/go1.26.4/bin:$PATH" go vet ./... && PATH="$HOME/sdk/go1.26.4/bin:$PATH" golangci-lint run ./...
```
Expected: no output from `gofmt -l`, no vet/lint findings.

- [ ] **Step 3: Run the full frontend suite, type-check, and lint**

```bash
cd stack/frontend && npx vitest run && npx tsc --noEmit && npm run lint
```
Expected: all green.

- [ ] **Step 4: End-to-end smoke (manual, requires the local stack)**

Use the `run` or `verify` skill (or `docker-compose up`) to bring up postgres + backend + frontend, open the app, and play an imported video. Confirm:
1. The live caption visibly grows word-by-word for a whole sentence before it commits (no longer just the first few words).
2. Committed subtitle sections end at sentence boundaries, not mid-sentence.
3. When two statements concern the same fact, the fact-check region shows a single merged card with the shared claim/source, a "% corroborated" score, and both statements listed; clicking a listed statement scrolls/highlights its subtitle.

If the caption still does not stream, capture the AssemblyAI session frames (the backend logs Turn messages) to check whether partials are arriving; only if v3 withholds partials add `format_turns` to `assemblyAIURL` and its test. Record the finding in the `go` or `nextjs` skill if it changes guidance.

- [ ] **Step 5: Final commit (only if Step 4 required a code change)**

```bash
git add -A
git commit -m "fix(live): <describe any e2e adjustment>"
```

---

## Self-review notes (author)

- **Spec coverage:** (1) real-time caption → Task 2 (turn silence) + Task 6 e2e; (2) sentence-aware cuts → Task 1; (3) group by matched claim/source → Task 3; (4) grouped display with aggregated enwiki score under subtitles → Tasks 4-5. All four spec sections map to tasks.
- **Type consistency:** `deriveFactCheckGroups` / `FactCheckGroup` / `FactCheckGroupMember` are defined in Task 3 and consumed unchanged in Tasks 4-5. `endsAtSentenceBoundary` (free func) and `(*liveUnit).endsAtSentenceBoundary` (method) are both introduced in Task 1 and named consistently. `LiveFactCheckGroups` props (`groups`, `selectedStatementId`, `onSelect`) match the panel call site.
- **No placeholders:** every code and command step contains the literal content/commands to run.
- **Out of scope held:** no entity grouping, no LLM boundary detection, no backend grouping events.
