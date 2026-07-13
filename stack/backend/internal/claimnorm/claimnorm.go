// Package claimnorm holds the normalisation shared by every claim-corpus producer:
// canonicalising the review URL that is the cross-path dedup key, and recognizing a
// structured-data licence that forbids reuse. Both live here so the Google API, the
// DataCommons feed, the ClaimReview outlet reader, and the ClaimsKG seed behave
// identically — a claim several paths carry collapses to one political_claims row,
// and a review under a no-reuse licence is dropped the same way everywhere.
package claimnorm

import (
	"net/url"
	"sort"
	"strings"
)

// trackingParams are query keys that never identify a distinct review, only the
// referrer/campaign, so they are stripped before the dedup key is formed. Any key
// with the "utm_" prefix is dropped in addition to these.
var trackingParams = map[string]struct{}{
	"fbclid": {}, "gclid": {}, "mc_cid": {}, "mc_eid": {}, "igshid": {},
	"ref": {}, "ref_src": {}, "_ga": {}, "spm": {}, "cmpid": {},
}

// isTracking reports whether a query key is a tracking/campaign parameter (a known
// key or any utm_* variant), so it is stripped before forming the dedup key.
func isTracking(key string) bool {
	k := strings.ToLower(key)
	if strings.HasPrefix(k, "utm_") {
		return true
	}
	_, ok := trackingParams[k]
	return ok
}

// CanonicalURL normalises a review URL so cosmetically different spellings of the
// same page produce one dedup key: scheme folded to https and lower-cased, host
// lower-cased with the default port dropped, fragment removed, a trailing slash
// removed (except root), and tracking query params stripped with the remainder
// sorted. An unparsable or scheme-less value is returned trimmed and unchanged, so a
// non-URL id still round-trips.
func CanonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "http" {
		u.Scheme = "https"
	}
	u.Host = strings.ToLower(u.Host)
	if p := u.Port(); p == "80" || p == "443" {
		u.Host = u.Hostname()
	}
	u.Fragment = ""
	u.RawFragment = ""
	if len(u.Path) > 1 {
		if trimmed := strings.TrimRight(u.Path, "/"); trimmed != "" {
			u.Path = trimmed
		} else {
			u.Path = "/"
		}
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if isTracking(k) {
				q.Del(k)
			}
		}
		u.RawQuery = encodeSorted(q)
	}
	return u.String()
}

// encodeSorted encodes query values with keys in sorted order (url.Values.Encode
// already sorts, but this keeps the intent explicit and stable).
func encodeSorted(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}

// DefaultRestrictiveLicenses are sdLicense URL substrings that forbid the reuse this
// ingest would make; a record declaring one is skipped even though only categorical
// fields are stored, out of caution.
var DefaultRestrictiveLicenses = []string{"by-nd", "by-nc-nd", "/nd/", "noderiv", "all-rights-reserved"}

// LicenseRestricted reports whether an sdLicense URL matches any of the given
// reuse-forbidding substrings (case-insensitive). An empty licence is never
// restricted. A nil denylist falls back to DefaultRestrictiveLicenses.
func LicenseRestricted(license string, denylist []string) bool {
	if strings.TrimSpace(license) == "" {
		return false
	}
	if denylist == nil {
		denylist = DefaultRestrictiveLicenses
	}
	l := strings.ToLower(license)
	for _, bad := range denylist {
		if strings.Contains(l, bad) {
			return true
		}
	}
	return false
}
