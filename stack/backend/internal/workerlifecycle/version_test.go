package workerlifecycle

import (
	"slices"
	"testing"
)

func TestVersionedQueueName(t *testing.T) {
	t.Parallel()
	if got := VersionedQueueName("embedding.jobs", "3"); got != "embedding.jobs.v3" {
		t.Fatalf("VersionedQueueName = %q, want embedding.jobs.v3", got)
	}
}

func TestActiveVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "single", raw: "1", want: "1", ok: true},
		{name: "newest is last", raw: "1,2,3", want: "3", ok: true},
		{name: "trailing comma ignored", raw: "1,2,", want: "2", ok: true},
		{name: "spaces trimmed", raw: " 1 , 2 ", want: "2", ok: true},
		{name: "empty", raw: "", want: "", ok: false},
		{name: "only commas", raw: ",,", want: "", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ActiveVersion(tc.raw)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("ActiveVersion(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNewestVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		versions []string
		want     string
	}{
		{name: "single", versions: []string{"1"}, want: "1"},
		{name: "numeric beats lexicographic", versions: []string{"2", "10"}, want: "10"},
		{name: "numeric unordered", versions: []string{"10", "2", "9"}, want: "10"},
		{name: "date stamps lexicographic", versions: []string{"20260101", "20260407", "20251231"}, want: "20260407"},
		{name: "non-numeric falls back to lexicographic", versions: []string{"a", "b", "ab"}, want: "b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NewestVersion(tc.versions); got != tc.want {
				t.Fatalf("NewestVersion(%v) = %q, want %q", tc.versions, got, tc.want)
			}
		})
	}
}

func TestQueueVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		qn   string
		base string
		want string
		ok   bool
	}{
		{name: "match", qn: "embedding.jobs.v3", base: "embedding.jobs", want: "3", ok: true},
		{name: "date version", qn: "embedding.jobs.v20260407", base: "embedding.jobs", want: "20260407", ok: true},
		{name: "wrong base", qn: "other.jobs.v3", base: "embedding.jobs", ok: false},
		{name: "no version marker", qn: "embedding.jobs", base: "embedding.jobs", ok: false},
		{name: "empty token", qn: "embedding.jobs.v", base: "embedding.jobs", ok: false},
		{name: "invalid token char", qn: "embedding.jobs.v3.dlq", base: "embedding.jobs", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := queueVersion(tc.qn, tc.base)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("queueVersion(%q,%q) = (%q,%v), want (%q,%v)", tc.qn, tc.base, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestVersionedDepths(t *testing.T) {
	t.Parallel()
	depths := map[string]int64{
		"embedding.jobs.v1": 5,
		"embedding.jobs.v2": 0,
		"embedding.jobs":    9, // unversioned, ignored
		"other.v1":          7, // wrong base, ignored
	}
	got := versionedDepths(depths, "embedding.jobs")
	want := map[string]int64{"1": 5, "2": 0}
	if len(got) != len(want) {
		t.Fatalf("versionedDepths size = %d, want %d (%v)", len(got), len(want), got)
	}
	for v, d := range want {
		if got[v] != d {
			t.Fatalf("versionedDepths[%q] = %d, want %d", v, got[v], d)
		}
	}
}

func TestIsDigits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"123", true}, {"0", true}, {"", false}, {"1a", false}, {"-1", false},
	} {
		if got := isDigits(tc.in); got != tc.want {
			t.Fatalf("isDigits(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsVersionToken(t *testing.T) {
	t.Parallel()
	valid := []string{"1", "v2", "20260407", "rc_1", "a-b"}
	invalid := []string{"", "1.2", "a b", "x/y"}
	for _, v := range valid {
		if !isVersionToken(v) {
			t.Fatalf("isVersionToken(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if isVersionToken(v) {
			t.Fatalf("isVersionToken(%q) = true, want false", v)
		}
	}
}

// guard against an accidental change to the exported sort contract used by logs.
func TestRetirableVersionsSorted(t *testing.T) {
	t.Parallel()
	got := RetirableVersions(map[string]int64{
		"embedding.jobs.v3": 0,
		"embedding.jobs.v1": 0,
		"embedding.jobs.v2": 0, // newest, never retirable
	}, "embedding.jobs")
	if !slices.IsSorted(got) {
		t.Fatalf("RetirableVersions not sorted: %v", got)
	}
}
