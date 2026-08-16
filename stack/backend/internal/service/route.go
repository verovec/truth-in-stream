package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/claimtype"
	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// Router is the context-aware retrieval layer of the political verify path: it
// routes one atomic claim, by its classifier type, to the authoritative source
// adapter(s) that can answer it, and broadens to web search when the preferred
// adapter comes back thin (the verifier, not retrieval, is the precision filter).
// It is the seam the umbrella design calls "route + retrieve": a statistic goes
// to the stats pack and comes back as a full series so the verifier can see a
// cherry-pick, a voting-record claim to the structured voting pack, an
// attribution to the press pack, and everything open-ended (causal, comparative,
// and any unrecognized type) to web search.
//
// The Router holds source.Retriever adapters keyed by their advertised Kind - a
// registry, not a switch ladder, so a new source family is a new constant plus a
// new adapter at wiring time and never an edit here. It depends only on the
// jurisdiction-agnostic source contract and the claimtype enum; it holds no
// transport or HTTP types and makes no outbound call of its own. The capstone
// (card L) wires it behind FACTCHECK_POLITICAL; with the flag off it is never
// constructed and the live path is exactly as before.
type Router struct {
	retrievers map[source.Kind]source.Retriever
	web        source.Retriever
	minResults int
	lang       string
}

// RouterConfig bounds a Router. MinResults is the floor below which a preferred
// adapter's result is considered thin and the router broadens to web search; it
// must be positive. Lang is the BCP-47 language threaded onto every source query
// (empty lets each adapter use its own default, which the French packs set to
// "fr").
type RouterConfig struct {
	MinResults int
	Lang       string
}

// defaultRoutes maps a verifiable claim type to the source families that answer
// it, most authoritative first. A type absent from the map (and any verifiable
// type whose preferred adapter is thin) falls through to web search, the
// open-ended catch-all. Non-verifiable types (promise, opinion) are not routed at
// all - claimtype.Type.Verifiable gates them out before this map is consulted.
var defaultRoutes = map[claimtype.Type][]source.Kind{
	claimtype.Statistic:    {source.KindStats},
	claimtype.VotingRecord: {source.KindVotingRecord},
	claimtype.Attribution:  {source.KindAttribution},
	claimtype.Causal:       {source.KindWebSearch},
	claimtype.Comparative:  {source.KindWebSearch},
}

// NewRouter builds a Router over the given retrievers, indexed by their advertised
// Kind. It fails when two retrievers advertise the same Kind (an ambiguous
// registry), when no web-search retriever is supplied (the fallback every
// open-ended claim and every thin result depends on), or when MinResults is not
// positive (a non-positive floor would make every result thin and stampede the
// web fallback). The routing table is fixed (defaultRoutes); a future pack adds a
// Kind constant and a route entry, not a Router field.
func NewRouter(retrievers []source.Retriever, cfg RouterConfig) (*Router, error) {
	if cfg.MinResults < 1 {
		return nil, fmt.Errorf("service: router min results must be positive, got %d", cfg.MinResults)
	}
	byKind := make(map[source.Kind]source.Retriever, len(retrievers))
	for _, r := range retrievers {
		if r == nil {
			return nil, fmt.Errorf("service: router retriever is nil")
		}
		if _, dup := byKind[r.Kind()]; dup {
			return nil, fmt.Errorf("service: router has two retrievers for kind %q", r.Kind())
		}
		byKind[r.Kind()] = r
	}
	web, ok := byKind[source.KindWebSearch]
	if !ok {
		return nil, fmt.Errorf("service: router requires a %q retriever for the web fallback", source.KindWebSearch)
	}
	return &Router{
		retrievers: byKind,
		web:        web,
		minResults: cfg.MinResults,
		lang:       cfg.Lang,
	}, nil
}

// Retrieve routes the claim by its type to the preferred adapter(s) and returns
// the evidence passages the verifier reads. A non-verifiable claim type (promise,
// opinion) and a blank claim retrieve nothing without any adapter call. Otherwise
// the preferred adapters for the type are queried in order with the
// coreference-resolved claim text and the caller's structured hints (a stats
// series key, a resolved voting selector); when their combined yield is below
// MinResults the router broadens to web search and merges its passages in.
// Evidence is deduplicated by its stable EvidenceID so a passage re-surfaced by
// the fallback is never handed to the verifier twice.
//
// A preferred adapter's error is not fatal as long as another source can answer:
// it is treated as a thin result so a failing authoritative API broadens to the
// open web rather than failing the claim. An error is returned only when nothing
// was retrieved at all and every source that ran failed - including the case
// where web search is itself the preferred adapter and it errored, so a total
// retrieval failure is never silently reported as "no evidence".
func (r *Router) Retrieve(ctx context.Context, claim string, ct claimtype.Type, hints map[string]string) ([]source.Evidence, error) {
	if strings.TrimSpace(claim) == "" || !ct.Verifiable() {
		return nil, nil
	}

	q := source.Query{Text: claim, Lang: r.lang, Hints: hints}

	out := make([]source.Evidence, 0, r.minResults)
	seen := make(map[string]struct{})
	var lastErr error
	webRan := false
	for _, kind := range preferredKinds(ct) {
		retriever, ok := r.retrievers[kind]
		if !ok {
			continue
		}
		if kind == source.KindWebSearch {
			webRan = true
		}
		evidence, err := retriever.Retrieve(ctx, q)
		if err != nil {
			// A preferred-source failure broadens to web rather than failing the
			// claim; the error is held (not dropped) so a total failure can still be
			// surfaced when no source answered.
			lastErr = err
			continue
		}
		out = appendUnique(out, seen, evidence)
	}

	if len(out) >= r.minResults {
		return out, nil
	}

	// Thin (or empty) preferred result: broaden to web search, the
	// precision-deferred fallback. When web was itself the preferred adapter it has
	// already run; skip the redundant second call.
	if !webRan {
		evidence, err := r.web.Retrieve(ctx, q)
		if err != nil {
			lastErr = err
		} else {
			out = appendUnique(out, seen, evidence)
		}
	}

	// Surface a total retrieval failure: nothing was retrieved and at least one
	// source that ran errored. A genuine empty result (no source errored) returns
	// the empty slice and no error.
	if len(out) == 0 && lastErr != nil {
		return nil, fmt.Errorf("service: routing %q claim retrieval: %w", ct, lastErr)
	}
	return out, nil
}

// preferredKinds returns the source families to query for a claim type, most
// authoritative first. A type with no dedicated route (claimtype.DefaultType and
// any future type not yet given a source) falls back to web search.
func preferredKinds(ct claimtype.Type) []source.Kind {
	if kinds, ok := defaultRoutes[ct]; ok {
		return kinds
	}
	return []source.Kind{source.KindWebSearch}
}

// appendUnique appends each piece of evidence whose EvidenceID has not been seen,
// updating seen. It is how the router keeps a passage re-surfaced by the web
// fallback from reaching the verifier twice. Evidence with no source family on its
// id (a zero-value EvidenceID) is dropped: it cannot be cited, the verifier's
// citation guard would reject it, and admitting it would collapse every
// uncited passage onto one dedup key.
func appendUnique(out []source.Evidence, seen map[string]struct{}, evidence []source.Evidence) []source.Evidence {
	for _, e := range evidence {
		if e.ID.Kind == "" {
			continue
		}
		id := e.ID.String()
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, e)
	}
	return out
}

// EvidencePassagesFrom projects retrieved source evidence into the verify path's
// EvidencePassage shape, carrying each passage's stable evidence id as a string
// so a verifier citation round-trips back to the source via
// source.ParseEvidenceID. It is the seam between the routing layer's source.Evidence
// and the verifier's passage contract; the capstone (card L) calls it before
// handing evidence to the verifier.
func EvidencePassagesFrom(evidence []source.Evidence) []EvidencePassage {
	passages := make([]EvidencePassage, 0, len(evidence))
	for _, e := range evidence {
		passages = append(passages, EvidencePassage{ID: e.ID.String(), Text: e.Passage, Date: e.Source.Date})
	}
	return passages
}
