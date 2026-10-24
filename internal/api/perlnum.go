package api

import (
	"math"
	"strconv"
	"strings"
)

// perlNumber is a string numified the way Perl does it.
type perlNumber struct {
	i     int64
	f     float64
	isInt bool // Perl keeps the value as an integer (IV)
}

// perlNum numifies s like Perl: leading whitespace, then the longest decimal
// number prefix (sign, digits, fraction, exponent) or Inf/NaN; anything else
// is 0. "60abc" is 60, "abc" and "0x10" are 0.
func perlNum(s string) perlNumber {
	s = strings.TrimLeft(s, " \t\n\r\f\v")
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	rest := strings.ToLower(s[i:])
	for _, w := range []string{"infinity", "inf", "nan"} {
		if strings.HasPrefix(rest, w) {
			f := math.Inf(1)
			if w == "nan" {
				f = math.NaN()
			} else if s[0] == '-' {
				f = math.Inf(-1)
			}
			return perlNumber{f: f}
		}
	}
	digits := func() int {
		n := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			n++
		}
		return n
	}
	n := digits()
	isInt := true
	if i < len(s) && s[i] == '.' {
		save := i
		i++
		if m := digits(); n+m == 0 {
			i = save
		} else {
			isInt = false
		}
	}
	if n == 0 && isInt {
		return perlNumber{isInt: true}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		save := i
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			i = save
		} else {
			isInt = false
		}
	}
	num := s[:i]
	if isInt {
		if v, err := strconv.ParseInt(num, 10, 64); err == nil {
			return perlNumber{i: v, f: float64(v), isInt: true}
		}
	}
	f, _ := strconv.ParseFloat(num, 64) // out of range gives ±Inf, as Perl
	return perlNumber{f: f}
}

// perlMul is Perl's numeric a * b: integer when both are integers and the
// product fits, otherwise floating point.
func perlMul(a, b perlNumber) perlNumber {
	if a.isInt && b.isInt {
		p := a.i * b.i
		if a.i == 0 || (p/a.i == b.i && !(a.i == -1 && b.i == math.MinInt64)) {
			return perlNumber{i: p, f: float64(p), isInt: true}
		}
	}
	return perlNumber{f: a.f * b.f}
}

// String formats n as Perl stringifies a number (%.15g for floats).
func (n perlNumber) String() string {
	switch {
	case n.isInt:
		return strconv.FormatInt(n.i, 10)
	case math.IsNaN(n.f):
		return "NaN"
	case math.IsInf(n.f, 1):
		return "Inf"
	case math.IsInf(n.f, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(n.f, 'g', 15, 64)
}
