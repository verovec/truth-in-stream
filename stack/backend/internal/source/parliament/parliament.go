// Package parliament ingests French parliamentary open data into the fact-check
// corpus through the source-connector framework (internal/connector). Textual
// records - Assemblee Nationale amendments, written questions and their answers,
// and seance debate records, plus Senat written questions and legislative
// dossiers - render into attributed French evidence passages published as the
// generic connector.EvidenceJob, drained by the generic evidence worker
// (cmd/evidenceworker) into evidence_chunks. Senat roll-call votes render into
// domain.VotingRecord rows (chamber=senat) published through the existing scrutins
// job, so a claim about how a senator voted, or an amendment's fate, is checked
// against the primary record.
//
// # Bulk dumps and incremental diffing
//
// Each source publishes a bulk dump (a per-legislature zip of per-record JSON/XML,
// a rolling CSV extract, or a PostgreSQL dump). A naive run would re-ingest the
// whole corpus every day, so the producer diffs by identifier against a persisted
// [Manifest]: it fingerprints every record in the fresh dump and republishes only
// the records whose fingerprint is new or changed since the last run. A
// conditional GET (ETag/Last-Modified) short-circuits the whole download when the
// dump itself is unchanged, so a same-day re-run does no work at all.
//
// # Verified formats only
//
// Each dataset's wire format is captured from a real downloaded sample before a
// parser is written (the repository rule that fixtures match the real wire format,
// never hand-guessed). See docs/fact-check-sources.md for the source inventory and
// licences.
package parliament

import (
	"fmt"
	"strings"
)

// Dataset ids. A dataset is one parliamentary open-data family with its own bulk
// dump, parser, source name, and registry descriptor.
const (
	// DatasetANAmendements is the Assemblee Nationale amendments dataset (the
	// per-legislature Amendements.json.zip bulk dump).
	DatasetANAmendements = "an-amendements"
	// DatasetANQuestions is the AN written-questions dataset (Questions_ecrites.json.zip).
	DatasetANQuestions = "an-questions"
	// DatasetANComptesRendus is the AN seance debate-records dataset (syseron.xml.zip).
	DatasetANComptesRendus = "an-comptesrendus"
	// DatasetSenatQuestions is the Senat written-questions CSV extract.
	DatasetSenatQuestions = "senat-questions"
	// DatasetSenatDosleg is the Senat legislative-dossiers dataset (the loi table of
	// the Senat dosleg PostgreSQL dump).
	DatasetSenatDosleg = "senat-dosleg"
	// DatasetSenatScrutins is the Senat roll-call votes dataset (the scr/votsen/
	// posvot/auteur tables of the same dosleg dump). It targets voting_records, not
	// the evidence corpus.
	DatasetSenatScrutins = "senat-scrutins"
)

// target is what a dataset's records land in: the evidence corpus or the voting
// store. It selects which producer and which queue the connector uses.
type target int

const (
	targetEvidence target = iota
	targetVoting
)

// datasetSpec is the pure-data declaration of one dataset family: how to locate
// its dump, where its records land, and how to turn the downloaded archive into
// records. Exactly one of extract (evidence) or extractVotes (voting) is set,
// matching target.
type datasetSpec struct {
	// source is the discriminator every record of this dataset carries (the
	// evidence Source, or the scrutin-id namespace for a voting dataset); it matches
	// the connector descriptor Name.
	source string
	// urlTemplate is the bulk-dump URL. A "%s" is filled with the legislature; a URL
	// without one (a Senat rolling export) is used verbatim.
	urlTemplate string
	// scope renders the human-readable run scope for the alerts.
	scope func(legislature string) string
	// target selects the corpus and producer.
	target target
	// extract turns a downloaded evidence archive (temp file path) into records.
	extract func(source, archivePath string) ([]record, error)
	// extractVotes turns a downloaded voting archive into per-scrutin vote payloads,
	// bounded to sessions on or after sinceYear (0 = every session).
	extractVotes func(archivePath string, sinceYear int) ([]scrutinPayload, error)
}

// specs is the dataset registry: the one table mapping a dataset id to its dump
// location and corpus placement. Adding a verified dataset is one entry here plus
// its parser.
var specs = map[string]datasetSpec{
	DatasetANAmendements: {
		source:      "an-amendements",
		urlTemplate: "https://data.assemblee-nationale.fr/static/openData/repository/%s/loi/amendements_div_legis/Amendements.json.zip",
		scope:       func(l string) string { return "AN amendements, legislature " + l },
		target:      targetEvidence,
		extract:     extractAmendements,
	},
	DatasetANQuestions: {
		source:      "an-questions",
		urlTemplate: "https://data.assemblee-nationale.fr/static/openData/repository/%s/questions/questions_ecrites/Questions_ecrites.json.zip",
		scope:       func(l string) string { return "AN questions ecrites, legislature " + l },
		target:      targetEvidence,
		extract:     extractQuestions,
	},
	DatasetANComptesRendus: {
		source:      "an-comptesrendus",
		urlTemplate: "https://data.assemblee-nationale.fr/static/openData/repository/%s/vp/syceronbrut/syseron.xml.zip",
		scope:       func(l string) string { return "AN comptes rendus de seance, legislature " + l },
		target:      targetEvidence,
		extract:     extractComptesRendus,
	},
	DatasetSenatQuestions: {
		source:      "senat-questions",
		urlTemplate: "https://data.senat.fr/data/questions/questions-depuis-un-an.csv",
		scope:       func(string) string { return "Senat questions ecrites (12 derniers mois)" },
		target:      targetEvidence,
		extract:     extractSenatQuestions,
	},
	DatasetSenatDosleg: {
		source:      "senat-dosleg",
		urlTemplate: "https://data.senat.fr/data/dosleg/dosleg.zip",
		scope:       func(string) string { return "Senat dossiers legislatifs" },
		target:      targetEvidence,
		extract:     extractSenatDosleg,
	},
	DatasetSenatScrutins: {
		source:       "senat-scrutins",
		urlTemplate:  "https://data.senat.fr/data/dosleg/dosleg.zip",
		scope:        func(string) string { return "Senat scrutins (votes)" },
		target:       targetVoting,
		extractVotes: extractSenatScrutins,
	},
}

// buildURL fills the legislature into the dump URL when the template carries a
// "%s" slot; a Senat rolling export with no slot is returned verbatim.
func buildURL(tmpl, legislature string) string {
	if strings.Contains(tmpl, "%s") {
		return fmt.Sprintf(tmpl, legislature)
	}
	return tmpl
}

// Datasets returns the ids of every wired dataset.
func Datasets() []string {
	out := make([]string, 0, len(specs))
	for id := range specs {
		out = append(out, id)
	}
	return out
}

// SourceName returns the discriminator for a dataset, and whether it is known.
func SourceName(dataset string) (string, bool) {
	spec, ok := specs[dataset]
	if !ok {
		return "", false
	}
	return spec.source, true
}

// IsVotingDataset reports whether a dataset targets the voting store (published to
// the scrutins queue) rather than the evidence corpus. The cmd and scheduler layers
// use it to bind the right queue.
func IsVotingDataset(dataset string) bool {
	spec, ok := specs[dataset]
	return ok && spec.target == targetVoting
}

// lookupSpec returns the dataset spec or an error naming the unknown dataset, so a
// misconfigured PARLIAMENT_DATASET fails fast with a clear message.
func lookupSpec(dataset string) (datasetSpec, error) {
	spec, ok := specs[dataset]
	if !ok {
		return datasetSpec{}, fmt.Errorf("parliament: unknown dataset %q", dataset)
	}
	return spec, nil
}
