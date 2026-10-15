package bashparse

import (
	"math"
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// ParseSleep parses the arguments of `sleep` (argv[1:]). GNU semantics:
// NUMBER[smhd] (decimal, optional fraction/exponent), several arguments are
// summed, "infinity"/"inf" is +Inf. Any dynamic or unparsable argument, or no
// argument at all, yields {Seconds: 0, Known: false}.
func ParseSleep(args []model.Word) model.SleepInfo {
	total := 0.0
	n := 0
	for _, a := range args {
		if a.Dynamic {
			return model.SleepInfo{}
		}
		if a.Lit == "--" && n == 0 {
			continue
		}
		sec, ok := parseSleepDuration(a.Lit)
		if !ok {
			return model.SleepInfo{}
		}
		total += sec
		n++
	}
	if n == 0 {
		return model.SleepInfo{}
	}
	return model.SleepInfo{Seconds: total, Known: true}
}

func parseSleepDuration(s string) (float64, bool) {
	if strings.EqualFold(s, "infinity") || strings.EqualFold(s, "inf") {
		return math.Inf(1), true
	}
	mult := 1.0
	if len(s) > 1 {
		switch s[len(s)-1] {
		case 's':
			s = s[:len(s)-1]
		case 'm':
			mult, s = 60, s[:len(s)-1]
		case 'h':
			mult, s = 3600, s[:len(s)-1]
		case 'd':
			mult, s = 86400, s[:len(s)-1]
		}
	}
	if !plainDecimal(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v * mult, true
}

// plainDecimal accepts digits[.digits][(e|E)[+-]digits] and .digits forms.
func plainDecimal(s string) bool {
	i, digits := 0, 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		exp := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			exp++
		}
		if exp == 0 {
			return false
		}
	}
	return i == len(s)
}
