package domain

import "testing"

func TestLocaleLanguageCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		locale Locale
		want   string
	}{
		{"default is auto-detect", LocaleEnglish, ""},
		{"zero value is auto-detect", Locale(""), ""},
		{"french biases to fr", LocaleFrench, "fr"},
		{"unknown locale auto-detects", Locale("de"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.locale.LanguageCode(); got != tc.want {
				t.Errorf("LanguageCode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocaleIsFrench(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		locale Locale
		want   bool
	}{
		{"english is not french", LocaleEnglish, false},
		{"zero value is not french", Locale(""), false},
		{"french is french", LocaleFrench, true},
		{"unknown locale is not french", Locale("es"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.locale.IsFrench(); got != tc.want {
				t.Errorf("IsFrench() = %v, want %v", got, tc.want)
			}
		})
	}
}
