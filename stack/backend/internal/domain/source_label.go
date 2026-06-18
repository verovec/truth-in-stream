package domain

import "strings"

// The canonical French publisher labels shown on a verdict chip. They are the
// single shared vocabulary the wire carries (the handler computes the label
// here and serializes it, so the frontend renders the string rather than
// re-deriving it). New providers add a constant; existing ones are stable.
const (
	SourceLabelAssemblee = "Assemblée nationale"
	SourceLabelSenat     = "Sénat"
	SourceLabelParlement = "Parlement"
	SourceLabelINSEE     = "INSEE"
	SourceLabelEurostat  = "Eurostat"
	SourceLabelWikipedia = "Wikipédia"
	SourceLabelPresse    = "Presse"
	SourceLabelWeb       = "Recherche web"
)

// The evidence id kind prefixes minted by the retrieval layer (internal/source
// EvidenceID.Kind). They are the first ":"-separated component of a citation's
// evidence id and identify the provider that backed the verdict. They are kept
// in lockstep with the source.Kind constants but referenced as plain strings so
// the domain layer (the lowest layer) does not depend on internal/source.
const (
	evidenceKindVoting      = "voting"
	evidenceKindINSEE       = "insee"
	evidenceKindEurostat    = "eurostat"
	evidenceKindWebSearch   = "websearch"
	evidenceKindAttribution = "attribution"
)

// SourceProvenance is the viewer-facing provenance of a verdict's winning
// citation: a French publisher label and the canonical link a reader can verify
// it at. The zero value (empty Label) means the verdict carries no provider
// source - a curated borrow or a knowledge-only verdict - so the chip is omitted
// rather than rendered empty.
type SourceProvenance struct {
	Label string
	URL   string
}

// WinningSource derives the verdict chip's provenance from a claim's cited
// matches: the leading citation whose evidence id maps to a known provider, with
// its canonical link. Citations are ranked, so the first labeled one is the
// strongest provider-backed source. A verdict with no labeled citation (a
// curated borrow, or a knowledge-only verdict with no evidence) returns the zero
// value, so the chip is omitted.
func WinningSource(matches []SegmentMatch) SourceProvenance {
	for _, m := range matches {
		if label := sourceLabel(m); label != "" {
			return SourceProvenance{Label: label, URL: sourceURL(m)}
		}
	}
	return SourceProvenance{}
}

// sourceLabel maps one cited match to its French publisher label, or "" when the
// match carries no recognized provider. The provider comes from the evidence id
// kind; for a parliamentary vote the chamber is refined from the provenance,
// which is the only place assemblee vs senat is carried (the kind is "voting"
// for both chambers).
func sourceLabel(m SegmentMatch) string {
	switch evidenceKind(m.EvidenceID) {
	case evidenceKindVoting:
		return parliamentaryLabel(provenanceName(m))
	case evidenceKindINSEE:
		return SourceLabelINSEE
	case evidenceKindEurostat:
		return SourceLabelEurostat
	case evidenceKindWebSearch:
		return SourceLabelWeb
	case evidenceKindAttribution:
		if name := provenanceName(m); name != "" {
			return name
		}
		return SourceLabelPresse
	case string(MatchKindEvidence):
		// The Wikipedia corpus mints its evidence ids under the generic evidence
		// kind; such a match carries an Article rather than a source pack id.
		return SourceLabelWikipedia
	default:
		// A curated claim ("claim"), an unknown kind, or a missing evidence id
		// carries no provider label; the curated/verified origin is shown
		// separately and a knowledge-only verdict shows no chip.
		return ""
	}
}

// sourceURL is the canonical link for a cited match: a Wikipedia article url, or
// else the first citation source url. It is empty when the match names no link.
func sourceURL(m SegmentMatch) string {
	if m.Article != nil && m.Article.URL != "" {
		return m.Article.URL
	}
	for _, s := range m.Sources {
		if s.URL != "" {
			return s.URL
		}
	}
	return ""
}

// provenanceName is the publisher name carried on a match's first citation
// source, or "" when none is named.
func provenanceName(m SegmentMatch) string {
	for _, s := range m.Sources {
		if s.Title != "" {
			return s.Title
		}
	}
	return ""
}

// evidenceKind returns the kind prefix of an evidence id (the text before the
// first separator), or "" when the id is empty or has no separator. It does not
// validate the kind, so it works for both the source-pack kinds (voting, insee,
// ...) and the domain MatchKind ids (claim, evidence).
func evidenceKind(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[:i]
	}
	return ""
}

// parliamentaryLabel resolves a recorded vote's provenance name to its chamber
// label, accent- and case-insensitively, falling back to a generic
// parliamentary label when the chamber is not recognizable.
func parliamentaryLabel(provenance string) string {
	switch n := foldAccents(strings.ToLower(provenance)); {
	case strings.Contains(n, "senat"):
		return SourceLabelSenat
	case strings.Contains(n, "assemblee"):
		return SourceLabelAssemblee
	default:
		return SourceLabelParlement
	}
}

// foldAccents strips the French accents that appear in chamber names so a name
// like "Sénat" matches its accent-free form. It is intentionally narrow: it
// folds only the vowels the chamber labels use.
func foldAccents(s string) string {
	return strings.NewReplacer(
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"à", "a", "â", "a",
	).Replace(s)
}
