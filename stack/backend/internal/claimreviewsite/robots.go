package claimreviewsite

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
)

// robots is the parsed subset of a robots.txt this reader honors: the Disallow
// path prefixes that apply to our user-agent (falling back to the "*" group) and
// the Crawl-delay. It is deliberately conservative — an unparsable or missing
// robots.txt yields an empty policy that allows everything, but any Disallow it
// does declare is enforced, and any Crawl-delay it sets floors the pacing.
type robots struct {
	disallow   []string
	crawlDelay time.Duration
}

// parseRobots reads a robots.txt and returns the rules that apply to userAgent,
// preferring an exact user-agent group over the "*" group. Only Disallow and
// Crawl-delay are interpreted; everything else is ignored. Grouping follows the
// standard: consecutive User-agent lines share the directives that follow them.
func parseRobots(r io.Reader, userAgent string) robots {
	ua := strings.ToLower(userAgent)
	var starRules, uaRules robots
	var haveUA bool

	sc := bufio.NewScanner(r)
	// Track which agents the current directive block applies to.
	var agents []string
	pendingAgents := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		field, value, ok := splitDirective(line)
		if !ok {
			continue
		}
		switch field {
		case "user-agent":
			if !pendingAgents {
				agents = nil
				pendingAgents = true
			}
			agents = append(agents, strings.ToLower(value))
		case "disallow", "crawl-delay":
			pendingAgents = false
			for _, a := range agents {
				applyRule(&starRules, &uaRules, &haveUA, a, ua, field, value)
			}
		}
	}
	if haveUA {
		return uaRules
	}
	return starRules
}

func applyRule(star, uaRules *robots, haveUA *bool, agent, ua, field, value string) {
	var target *robots
	switch {
	case agent == "*":
		target = star
	case agent == ua || strings.HasPrefix(ua, agent):
		target = uaRules
		*haveUA = true
	default:
		return
	}
	switch field {
	case "disallow":
		if value != "" {
			target.disallow = append(target.disallow, value)
		}
	case "crawl-delay":
		if secs, err := strconv.ParseFloat(value, 64); err == nil && secs > 0 {
			target.crawlDelay = time.Duration(secs * float64(time.Second))
		}
	}
}

func splitDirective(line string) (field, value string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(line[:i])), strings.TrimSpace(line[i+1:]), true
}

// allowed reports whether path (the request URI path) may be fetched under these
// rules: it is disallowed when any Disallow pattern matches. Patterns honor the
// robots.txt wildcard syntax '*' (any run of characters, including none) and '$'
// (end-of-path anchor) that lemonde.fr and francetvinfo.fr actually use, so a
// wildcard Disallow is not mistaken for allow-all. An empty Disallow set allows
// everything.
func (r robots) allowed(path string) bool {
	for _, d := range r.disallow {
		if robotPatternMatches(d, path) {
			return false
		}
	}
	return true
}

// robotPatternMatches reports whether a robots.txt path pattern matches (i.e.
// governs) path. A pattern matches a prefix of the path unless it ends with '$'
// (which anchors the match to the end of the path); '*' matches any run of
// characters. Matching is anchored at the start of the path, per the spec. A
// non-anchored rule is turned into a whole-string match by allowing any suffix,
// which keeps the matcher a single non-backtracking pass.
func robotPatternMatches(pattern, path string) bool {
	if strings.HasSuffix(pattern, "$") {
		pattern = pattern[:len(pattern)-1]
	} else {
		// Without '$' a rule matches a prefix of the path: allow any trailing suffix.
		pattern += "*"
	}
	return globMatch(pattern, path)
}

// globMatch reports whether pattern matches the whole of s, with '*' matching any
// run of characters (including none). It is the standard two-pointer greedy
// algorithm with O(1) star backtracking (remembering only the last '*'), so it runs
// in linear-time with NO exponential blowup. This matters because robots.txt is
// fetched from each outlet's live, externally-controlled site: a recursive
// backtracking matcher could be driven into catastrophic backtracking by a hostile
// or misconfigured robots.txt and hang the crawler goroutine (pure CPU, no
// cancellation); this matcher cannot.
func globMatch(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			// Backtrack only the most recent '*', consuming one more character of s.
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
