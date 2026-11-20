package claimnorm

import "testing"

func TestCanonicalURL(t *testing.T) {
	t.Parallel()
	// Every input below should canonicalise to this one dedup key.
	const want = "https://factuel.afp.com/article-x?a=1&b=2"
	cases := []string{
		"https://factuel.afp.com/article-x?a=1&b=2",
		"http://factuel.afp.com/article-x?a=1&b=2",                                // scheme fold
		"https://Factuel.AFP.com/article-x?a=1&b=2",                               // host case
		"https://factuel.afp.com/article-x/?a=1&b=2",                              // trailing slash
		"https://factuel.afp.com/article-x?b=2&a=1",                               // param order
		"https://factuel.afp.com/article-x?a=1&b=2#section",                       // fragment
		"https://factuel.afp.com:443/article-x?a=1&b=2",                           // default port
		"https://factuel.afp.com/article-x?a=1&b=2&utm_source=twitter&fbclid=xyz", // tracking
	}
	for _, in := range cases {
		if got := CanonicalURL(in); got != want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalURLRootAndNonURL(t *testing.T) {
	t.Parallel()
	if got := CanonicalURL("https://x.fr/"); got != "https://x.fr/" {
		t.Errorf("root path = %q, want https://x.fr/", got)
	}
	// A non-URL id round-trips unchanged (trimmed).
	if got := CanonicalURL("  not a url  "); got != "not a url" {
		t.Errorf("non-url = %q, want 'not a url'", got)
	}
}

func TestLicenseRestricted(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"": false,
		"https://creativecommons.org/licenses/by/4.0/":      false,
		"https://creativecommons.org/licenses/by-nd/4.0/":   true,
		"https://creativecommons.org/licenses/by-nc-nd/4.0": true,
		"All-Rights-Reserved":                               true,
	}
	for lic, want := range cases {
		if got := LicenseRestricted(lic, nil); got != want {
			t.Errorf("LicenseRestricted(%q) = %v, want %v", lic, got, want)
		}
	}
}
