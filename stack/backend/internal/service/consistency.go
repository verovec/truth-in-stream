package service

import (
	"context"
	"math"
	"sort"
	"sync"
)

// StanceClassifier is the pairwise stance port the live analyzer consults to
// decide whether a speaker contradicted an earlier statement of their own.
// Cosine similarity only reveals that a speaker returned to a topic; opposite
// assertions sit close in embedding space, so a genuine self-contradiction
// needs this pairwise judgment. Contradicts reports whether later contradicts
// earlier, with a short rationale set only when it does. An error means the
// judgment is unavailable and the caller degrades to no flag - it is defined
// consumer-side so the analyzer depends on the interface, not the LLM client.
type StanceClassifier interface {
	Contradicts(ctx context.Context, earlier, later string) (contradicts bool, rationale string, err error)
}

// ConsistencyFlag is the payload of a consistency live event: the speaker
// contradicted their own earlier statement. EarlierID and EarlierText identify
// the conflicting prior statement so the UI can link the flagged statement back
// to it; Speaker is the speaker both statements share; Rationale is the stance
// check's one-line reason.
type ConsistencyFlag struct {
	EarlierID   string
	EarlierText string
	Speaker     string
	Rationale   string
}

// priorStatement is one checkable statement a speaker made earlier in the
// session, retained so a later statement can be compared against it: its
// subtitle correlation id, its text, and the query embedding the matcher
// already computed for it (reused for cosine ranking, never re-embedded).
type priorStatement struct {
	id        string
	text      string
	embedding []float32
}

// speakerMemory is the per-session, in-process record of each speaker's prior
// checkable statements, keyed by normalized speaker label. It is created fresh
// inside LiveAnalyzer.Run and discarded when the stream ends, so a later
// session never sees a prior one's statements. The worker pool scores units
// concurrently, so every access is mutex-guarded.
type speakerMemory struct {
	mu        sync.Mutex
	bySpeaker map[string][]priorStatement
	locks     map[string]*sync.Mutex
	// credibility holds each speaker's running verdict tally, created lazily on a
	// speaker's first counted verdict. It lives here, per session, so a later
	// session never inherits a prior one's counts; mu guards it alongside the
	// consistency history.
	credibility map[string]*speakerCredibility
}

// newSpeakerMemory builds an empty per-session memory.
func newSpeakerMemory() *speakerMemory {
	return &speakerMemory{
		bySpeaker:   make(map[string][]priorStatement),
		locks:       make(map[string]*sync.Mutex),
		credibility: make(map[string]*speakerCredibility),
	}
}

// observeVerdict folds one claim verdict into the speaker's running tally and
// returns the single updated snapshot, stamped with the speaker. state moves the
// matching credibility count (the literal axis on the political path, mapped onto
// the credibility vocabulary); flagged bumps the orthogonal misleading-framing
// tally when the claim carried at least one manipulation flag. Both axes are folded
// under one held lock so a concurrently-counted claim for the same speaker can never
// observe a half-updated state, and exactly one snapshot is emitted per verdict. The
// aggregator is created on first use. Access is mutex-guarded because the worker pool
// counts a speaker's claims concurrently.
func (m *speakerMemory) observeVerdict(speaker, state string, flagged bool) SpeakerTally {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.credibility[speaker]
	if sc == nil {
		sc = &speakerCredibility{}
		m.credibility[speaker] = sc
	}
	snapshot := sc.observe(state)
	if flagged {
		snapshot = sc.observeFraming()
	}
	snapshot.Speaker = speaker
	return snapshot
}

// speakerLock returns the per-speaker detection lock, creating it on first use.
// Detection for one speaker is serialized under it so the prior-lookup, stance
// check, and append form one atomic step: without it two workers scoring the
// same speaker's back-to-back units could both observe an empty history and
// miss the contradiction between them. Different speakers take different locks
// and proceed concurrently. mu is a leaf lock here (acquired and released
// before the per-speaker lock is taken), so there is no lock-ordering cycle.
func (m *speakerMemory) speakerLock(speaker string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lk := m.locks[speaker]
	if lk == nil {
		lk = &sync.Mutex{}
		m.locks[speaker] = lk
	}
	return lk
}

// prior returns a snapshot of the speaker's earlier checkable statements. The
// returned slice is a copy of the header so the caller can rank it without
// holding the lock; the priorStatement values are read-only.
func (m *speakerMemory) prior(speaker string) []priorStatement {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.bySpeaker[speaker]
	if len(stored) == 0 {
		return nil
	}
	out := make([]priorStatement, len(stored))
	copy(out, stored)
	return out
}

// remember appends a statement to the speaker's history. It is called after the
// comparison so a statement is never compared against itself.
func (m *speakerMemory) remember(speaker string, s priorStatement) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bySpeaker[speaker] = append(m.bySpeaker[speaker], s)
}

// rankBySimilarity returns the prior statements whose embedding clears the floor
// against emb, ordered most similar first and capped at topK. It is the cheap
// first stage of detection: it narrows the same speaker's history to the few
// topically-related statements worth a stance call.
func rankBySimilarity(emb []float32, priors []priorStatement, floor float64, topK int) []priorStatement {
	type scored struct {
		stmt priorStatement
		sim  float64
	}
	candidates := make([]scored, 0, len(priors))
	for _, p := range priors {
		sim := cosineSimilarity(emb, p.embedding)
		if sim >= floor {
			candidates = append(candidates, scored{stmt: p, sim: sim})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})
	if topK > 0 && len(candidates) > topK {
		candidates = candidates[:topK]
	}
	ranked := make([]priorStatement, len(candidates))
	for i, c := range candidates {
		ranked[i] = c.stmt
	}
	return ranked
}

// cosineSimilarity is the cosine of the angle between two embeddings, in
// [-1, 1]; higher is more similar. It returns 0 for a length mismatch or a
// zero-magnitude vector, so a malformed embedding ranks as unrelated rather
// than poisoning the comparison.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		af, bf := float64(a[i]), float64(b[i])
		dot += af * bf
		normA += af * af
		normB += bf * bf
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
