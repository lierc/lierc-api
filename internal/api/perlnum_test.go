package api

import "testing"

func TestPerlNum(t *testing.T) {
	for in, want := range map[string]string{
		"": "0", "abc": "0", "60abc": "60", " 7": "7", "-3x": "-3", "1.5": "1.5", "1e1": "10",
		"1e": "1", ".5": "0.5", ".": "0", "0x10": "0", "inf": "Inf", "-Infinity": "-Inf", "nan": "NaN",
		"99999999999999999999": "1e+20",
	} {
		if got := perlNum(in).String(); got != want {
			t.Errorf("perlNum(%q) = %s, want %s", in, got, want)
		}
	}
	mul := func(a, b string) string { return perlMul(perlNum(a), perlNum(b)).String() }
	for _, c := range [][3]string{{"1.5", "15", "22.5"}, {"abc", "15", "0"}, {"3", "15", "45"}, {"1e1", "15", "150"},
		{"9223372036854775807", "2", "1.84467440737096e+19"}} {
		if got := mul(c[0], c[1]); got != c[2] {
			t.Errorf("%s * %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}
