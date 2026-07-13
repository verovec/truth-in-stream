package parliament

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// amendementURLTemplate builds the stable per-amendment open-data record URL
// (verified live during source research), used as each chunk's provenance link so
// a citation round-trips to the primary record.
const amendementURLTemplate = "https://www.assemblee-nationale.fr/dyn/opendata/%s.json"

// anAmendement is the subset of the Assemblee Nationale amendement open-data JSON
// this connector reads. Field names match the verified live record shape
// (data.assemblee-nationale.fr / dyn opendata); unread fields are omitted. The
// dispositif and exposeSommaire are HTML fragments the renderer flattens to plain
// French text.
type anAmendement struct {
	UID            string `json:"uid"`
	Legislature    string `json:"legislature"`
	Identification struct {
		NumeroLong          string `json:"numeroLong"`
		NumeroOrdreDepot    string `json:"numeroOrdreDepot"`
		PrefixeOrganeExamen string `json:"prefixeOrganeExamen"`
	} `json:"identification"`
	TexteLegislatifRef string `json:"texteLegislatifRef"`
	Signataires        struct {
		Auteur struct {
			TypeAuteur         string `json:"typeAuteur"`
			ActeurRef          string `json:"acteurRef"`
			GroupePolitiqueRef string `json:"groupePolitiqueRef"`
		} `json:"auteur"`
	} `json:"signataires"`
	PointeurFragmentTexte struct {
		Division struct {
			Titre                    string `json:"titre"`
			ArticleDesignationCourte string `json:"articleDesignationCourte"`
		} `json:"division"`
	} `json:"pointeurFragmentTexte"`
	Corps struct {
		ContenuAuteur struct {
			Dispositif     string `json:"dispositif"`
			ExposeSommaire string `json:"exposeSommaire"`
		} `json:"contenuAuteur"`
	} `json:"corps"`
	CycleDeVie struct {
		DateDepot string `json:"dateDepot"`
		Sort      string `json:"sort"`
		DateSort  string `json:"dateSort"`
	} `json:"cycleDeVie"`
}

// extractAmendements reads the amendments zip, parsing every .json entry (each
// entry may hold one or several amendement objects) into records.
func extractAmendements(source, archivePath string) ([]record, error) {
	return extractZipEntries(source, archivePath, ".json", parseAmendementEntry)
}

// parseAmendementEntry parses one amendments dump file into its records, unwrapping
// the entry's shape and parsing each amendement object.
func parseAmendementEntry(source string, raw []byte) ([]record, error) {
	objects, err := amendementEntries(raw)
	if err != nil {
		return nil, err
	}
	records := make([]record, 0, len(objects))
	for _, obj := range objects {
		rec, err := parseAmendement(source, obj)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// amendementEntries splits one dump file into its individual amendement objects,
// tolerating every shape the AN bulk archive has been observed to use: a bare
// object (top-level uid, as the per-record opendata endpoint serves), the
// {"amendement": {...}} envelope (mirroring the scrutins archive's
// {"scrutin": {...}} entries), the {"amendements": {"amendement": [...]}}
// aggregate, and a top-level array. It also absorbs the AN's single-element-as-
// object quirk (a one-element list serialized as a bare object) via
// rawSingleOrArray. Each returned raw object is fed to parseAmendement.
func amendementEntries(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("parliament: decode amendement array: %w", err)
		}
		return arr, nil
	}
	var probe struct {
		Amendements json.RawMessage `json:"amendements"`
		Amendement  json.RawMessage `json:"amendement"`
		UID         string          `json:"uid"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, fmt.Errorf("parliament: decode amendement entry: %w", err)
	}
	switch {
	case len(probe.Amendements) > 0:
		// Aggregate wrapper: recurse to unwrap the inner "amendement" list/object.
		return amendementEntries(probe.Amendements)
	case len(probe.Amendement) > 0:
		return rawSingleOrArray(probe.Amendement)
	case probe.UID != "":
		return []json.RawMessage{trimmed}, nil
	default:
		return nil, fmt.Errorf("parliament: amendement entry has neither a uid nor an amendement object")
	}
}

// rawSingleOrArray returns the elements of a JSON value that is either an array or,
// per the AN single-element-as-object quirk, a bare object standing for a
// one-element list. null or empty yields no elements.
func rawSingleOrArray(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("parliament: decode amendement list: %w", err)
		}
		return arr, nil
	}
	return []json.RawMessage{trimmed}, nil
}

// parseAmendement decodes one amendement object into a record: an attributed
// French lead passage (author, article, deposit date, and fate) followed by the
// flattened dispositif and expose sommaire, chunked to the corpus convention. The
// fate is always rendered even when the body text is empty, since "cet amendement
// a ete rejete" is itself the checkable fact. source is the evidence Source
// discriminator every chunk lands under.
func parseAmendement(source string, data []byte) (record, error) {
	var a anAmendement
	if err := json.Unmarshal(data, &a); err != nil {
		return record{}, fmt.Errorf("parliament: decode amendement: %w", err)
	}
	if a.UID == "" {
		return record{}, fmt.Errorf("parliament: amendement has empty uid")
	}

	url := fmt.Sprintf(amendementURLTemplate, a.UID)
	return buildEvidenceRecord(source, a.UID, amendementTitle(a), url, renderAmendement(a), amendementMetadata(a)), nil
}

// amendementNumero returns the most human-readable amendment number available.
func amendementNumero(a anAmendement) string {
	if a.Identification.NumeroLong != "" {
		return a.Identification.NumeroLong
	}
	return a.Identification.NumeroOrdreDepot
}

// amendementTitle names the chunk for citation display, degrading to the uid when
// no numero or division is present.
func amendementTitle(a anAmendement) string {
	numero := amendementNumero(a)
	division := a.PointeurFragmentTexte.Division.Titre
	switch {
	case numero != "" && division != "":
		return fmt.Sprintf("Amendement %s (%s)", numero, division)
	case numero != "":
		return "Amendement " + numero
	case division != "":
		return "Amendement " + a.UID + " (" + division + ")"
	default:
		return "Amendement " + a.UID
	}
}

// renderAmendement builds the attributed French passage: an attribution and fate
// header always present, then the flattened dispositif and expose sommaire when
// present.
func renderAmendement(a anAmendement) string {
	var b strings.Builder
	b.WriteString("Amendement ")
	b.WriteString(amendementNumero(a))
	if div := a.PointeurFragmentTexte.Division.Titre; div != "" {
		b.WriteString(" a ")
		b.WriteString(div)
	}
	if a.TexteLegislatifRef != "" {
		b.WriteString(" du texte ")
		b.WriteString(a.TexteLegislatifRef)
	}
	b.WriteString(".")
	if ref := a.Signataires.Auteur.ActeurRef; ref != "" {
		b.WriteString(" Auteur : ")
		b.WriteString(ref)
		if grp := a.Signataires.Auteur.GroupePolitiqueRef; grp != "" {
			b.WriteString(" (groupe ")
			b.WriteString(grp)
			b.WriteString(")")
		}
		b.WriteString(".")
	}
	if d := a.CycleDeVie.DateDepot; d != "" {
		b.WriteString(" Depose le ")
		b.WriteString(d)
		b.WriteString(".")
	}
	if s := a.CycleDeVie.Sort; s != "" {
		b.WriteString(" Sort : ")
		b.WriteString(s)
		if ds := a.CycleDeVie.DateSort; ds != "" {
			b.WriteString(" (")
			b.WriteString(ds)
			b.WriteString(")")
		}
		b.WriteString(".")
	}
	if disp := plainText(a.Corps.ContenuAuteur.Dispositif); disp != "" {
		b.WriteString(" Dispositif : ")
		b.WriteString(disp)
	}
	if expose := plainText(a.Corps.ContenuAuteur.ExposeSommaire); expose != "" {
		b.WriteString(" Expose sommaire : ")
		b.WriteString(expose)
	}
	return b.String()
}

// amendementMetadata renders the source-specific provenance carried verbatim as
// jsonb on every chunk. Only non-empty fields are written, so a record missing a
// field carries no null key.
func amendementMetadata(a anAmendement) map[string]any {
	meta := make(map[string]any)
	putMeta(meta, "legislature", a.Legislature)
	putMeta(meta, "numero", amendementNumero(a))
	putMeta(meta, "auteur_ref", a.Signataires.Auteur.ActeurRef)
	putMeta(meta, "groupe_ref", a.Signataires.Auteur.GroupePolitiqueRef)
	putMeta(meta, "article", a.PointeurFragmentTexte.Division.Titre)
	putMeta(meta, "texte_ref", a.TexteLegislatifRef)
	putMeta(meta, "sort", a.CycleDeVie.Sort)
	putMeta(meta, "date_depot", a.CycleDeVie.DateDepot)
	putMeta(meta, "date_sort", a.CycleDeVie.DateSort)
	return meta
}
