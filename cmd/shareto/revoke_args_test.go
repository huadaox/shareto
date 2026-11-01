package main

import "testing"

func TestParseRevokeArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantYes    bool
		wantTarget string
	}{
		{"token starting with dash", []string{"-RopwGSVcX4Nji5bzw-IKA.."}, false, "-RopwGSVcX4Nji5bzw-IKA.."},
		{"dash token with yes flag", []string{"-y", "-RopwGSVcX4Nji5bzw-IKA.."}, true, "-RopwGSVcX4Nji5bzw-IKA.."},
		{"plain token", []string{"abc123"}, false, "abc123"},
		{"long yes flag", []string{"--yes", "abc123"}, true, "abc123"},
		{"all flag after token", []string{"abc123", "--all"}, true, "abc123"},
		{"double dash ends flags", []string{"--", "-y"}, false, "-y"},
		{"double dash keeps flags after", []string{"-y", "--", "-abc"}, true, "-abc"},
		{"relative path", []string{"./a/report.txt"}, false, "./a/report.txt"},
		{"only flag", []string{"--yes"}, true, ""},
		{"no args", nil, false, ""},
	}

	for _, c := range cases {
		gotYes, gotTarget := parseRevokeArgs(c.args)
		if gotYes != c.wantYes || gotTarget != c.wantTarget {
			t.Errorf("%s: parseRevokeArgs(%q) = (%v, %q), want (%v, %q)",
				c.name, c.args, gotYes, gotTarget, c.wantYes, c.wantTarget)
		}
	}
}
