package config

import (
	"math"
	"strings"
	"time"
)

// The Phase-2 institutional evidence connectors (vie-publique discours, HATVP
// declarations, Legifrance code articles) share the generic evidence queue and
// worker; only their producer knobs differ. Their config loaders live here so the
// core config file stays focused. Each is keyless except Legifrance, whose PISTE
// OAuth2 credentials are read from env/secrets only.

// ViePublique configures the vie-publique discours producer. MaxItems bounds a
// backfill run (0 = unbounded); MarkerPath and ManifestPath are the per-source
// state files under the shared state volume.
type ViePublique struct {
	MaxItems     int
	MarkerPath   string
	ManifestPath string
}

// LoadViePublique reads the vie-publique producer configuration. VIEPUBLIQUE_MAX_ITEMS
// defaults to 0 (unbounded); the state paths are fixed under /state.
func LoadViePublique() (ViePublique, error) {
	maxItems, err := intEnv("VIEPUBLIQUE_MAX_ITEMS", 0, 0, math.MaxInt32)
	if err != nil {
		return ViePublique{}, err
	}
	return ViePublique{
		MaxItems:     maxItems,
		MarkerPath:   "/state/viepublique-marker.json",
		ManifestPath: "/state/viepublique-manifest.json",
	}, nil
}

// HATVP configures the HATVP declarations producer. MaxItems bounds a backfill run
// (0 = unbounded); MarkerPath and ManifestPath are the per-source state files.
type HATVP struct {
	MaxItems     int
	MarkerPath   string
	ManifestPath string
}

// LoadHATVP reads the HATVP producer configuration. HATVP_MAX_ITEMS defaults to 0
// (unbounded); the state paths are fixed under /state.
func LoadHATVP() (HATVP, error) {
	maxItems, err := intEnv("HATVP_MAX_ITEMS", 0, 0, math.MaxInt32)
	if err != nil {
		return HATVP{}, err
	}
	return HATVP{
		MaxItems:     maxItems,
		MarkerPath:   "/state/hatvp-marker.json",
		ManifestPath: "/state/hatvp-manifest.json",
	}, nil
}

// LegifranceArticle is one entry of the Legifrance starter corpus: the article id
// to fetch and an optional human label of the code it belongs to.
type LegifranceArticle struct {
	ID    string
	Label string
}

// Legifrance configures the Legifrance PISTE producer. ClientID and ClientSecret
// are the OAuth2 client-credentials read from env/secrets only (empty triggers the
// producer's graceful skip); TokenURL and APIBaseURL override the PISTE endpoints
// (empty uses production); Articles is the parsed starter corpus; MaxItems bounds
// a run; MinInterval paces requests to honor the quota; ManifestPath is the
// per-source state file.
type Legifrance struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	APIBaseURL   string
	Articles     []LegifranceArticle
	MaxItems     int
	MinInterval  time.Duration
	ManifestPath string
}

// LoadLegifrance reads the Legifrance producer configuration. LEGIFRANCE_CLIENT_ID
// and LEGIFRANCE_CLIENT_SECRET are the OAuth2 credentials (absent means a clean
// skip). LEGIFRANCE_ARTICLES is a comma-separated list of "LEGIARTI...=Label"
// entries (the label is optional); LEGIFRANCE_MAX_ITEMS bounds a run;
// LEGIFRANCE_MIN_INTERVAL_MS paces requests (default 500ms). The endpoints default
// to the PISTE production gateway.
func LoadLegifrance() (Legifrance, error) {
	maxItems, err := intEnv("LEGIFRANCE_MAX_ITEMS", 0, 0, math.MaxInt32)
	if err != nil {
		return Legifrance{}, err
	}
	intervalMS, err := intEnv("LEGIFRANCE_MIN_INTERVAL_MS", 500, 0, 600000)
	if err != nil {
		return Legifrance{}, err
	}
	return Legifrance{
		ClientID:     getenv("LEGIFRANCE_CLIENT_ID", ""),
		ClientSecret: getenv("LEGIFRANCE_CLIENT_SECRET", ""),
		TokenURL:     getenv("LEGIFRANCE_TOKEN_URL", ""),
		APIBaseURL:   getenv("LEGIFRANCE_API_BASE_URL", ""),
		Articles:     parseLegifranceArticles(getenv("LEGIFRANCE_ARTICLES", "")),
		MaxItems:     maxItems,
		MinInterval:  time.Duration(intervalMS) * time.Millisecond,
		ManifestPath: "/state/legifrance-manifest.json",
	}, nil
}

// parseLegifranceArticles parses the comma-separated "id=label" corpus list,
// dropping empty entries and trimming whitespace. An entry without "=" is an id
// with no label.
func parseLegifranceArticles(raw string) []LegifranceArticle {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	articles := make([]LegifranceArticle, 0, len(parts))
	for _, p := range parts {
		id, label, _ := strings.Cut(p, "=")
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		articles = append(articles, LegifranceArticle{ID: id, Label: strings.TrimSpace(label)})
	}
	return articles
}
