package redact

import (
	"strings"
	"sync"
)

// family is a bit set naming the rule families an input may trigger.
type family uint16

const (
	fPEM family = 1 << iota
	fAuth
	fBearer
	fBasic
	fKey // key_value and flag scanners
	fAWS
	fVendor
	fJWT
)

// anchors are lower-cased trigrams; every rule requires at least one of the
// anchors of its family to occur (case-insensitively) in any input it can
// match. The trigram table is built lazily (no init(), archtest
// TestNoHeavyPackageInit).
var anchors = [...]struct {
	tri string
	f   family
}{
	{"-be", fPEM}, // -----BEGIN
	{"riz", fAuth},
	{"rer", fBearer},
	{"asi", fBasic},
	// key_value / flag key words: password passwd passphrase pass, pwd,
	// secret, token, *key, credential, auth.
	{"pas", fKey}, {"pwd", fKey}, {"ecr", fKey}, {"tok", fKey},
	{"key", fKey}, {"ede", fKey}, {"aut", fKey},
	// AWS access key id prefixes (AKIA ASIA AGPA AIDA AROA AIPA ANPA ANVA
	// ABIA ACCA); confirmed case-sensitively before the pattern runs.
	{"kia", fAWS}, {"sia", fAWS}, {"gpa", fAWS}, {"ida", fAWS}, {"roa", fAWS},
	{"ipa", fAWS}, {"npa", fAWS}, {"nva", fAWS}, {"bia", fAWS}, {"cca", fAWS},
	// vendor tokens.
	{"hp_", fVendor}, {"ho_", fVendor}, {"hu_", fVendor}, {"hs_", fVendor}, {"hr_", fVendor}, // gh?_
	{"ub_", fVendor},                                     // github_pat_
	{"lpa", fVendor},                                     // glpat-
	{"npm", fVendor},                                     // npm_
	{"xox", fVendor},                                     // Slack
	{"sk-", fVendor}, {"sk_", fVendor}, {"rk_", fVendor}, // OpenAI/Anthropic, Stripe
	{"aiz", fVendor}, // AIza
	{"ya2", fVendor}, // ya29.
	{"hf_", fVendor}, // Hugging Face
	{"ypi", fVendor}, // pypi-
	{"ckr", fVendor}, // dckr_pat_
	{"_v1", fVendor}, // dop_v1_ ...
	{"sg.", fVendor}, // SendGrid
	{".sl", fVendor}, // hooks.slack.com/services/
	{"whs", fVendor}, // whsec_
	{"eyj", fJWT},
}

// The table is open-addressed by a multiplicative hash of the lower-cased
// trigram; each slot stores the exact trigram so unrelated text that hashes
// to the same slot is not reported (a 4 KiB clean input would otherwise
// trigger spuriously). buildAnchors panics if two anchors share a slot, and
// TestAnchorTable asserts the table is collision free.
type anchorSlot struct {
	key uint32 // lower-cased trigram, 0 when the slot is empty
	fam family
}

var (
	anchorOnce sync.Once
	anchorTab  [4096]anchorSlot
)

const hashMul = 0x9E3779B1

func lowByte(b byte) uint32 { return uint32(b | 0x20) }

func triHash(w uint32) uint32 { return (w * hashMul) >> 20 }

// triKey is the lower-cased trigram as a little-endian integer (first byte
// in the low bits), the same layout scanFlags extracts from a 64-bit load.
func triKey(t string) uint32 {
	return lowByte(t[0]) | lowByte(t[1])<<8 | lowByte(t[2])<<16
}

func buildAnchors() {
	for _, a := range anchors {
		w := triKey(a.tri)
		i := triHash(w)
		if anchorTab[i].fam != 0 && anchorTab[i].key != w {
			panic("redact: anchor hash collision for " + a.tri)
		}
		anchorTab[i].key = w
		anchorTab[i].fam |= a.f
	}
}

// scanFlags makes one pass over s and returns the families whose anchors
// occur (case-insensitively).
func scanFlags(s string) family {
	n := len(s)
	if n < 3 {
		return 0
	}
	anchorOnce.Do(buildAnchors)
	tab := &anchorTab
	var f family
	i := 0
	// Six trigram starts per 8-byte load.
	for ; i+8 <= n; i += 6 {
		x := uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
			uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
		x |= 0x2020202020202020
		for j := uint(0); j < 6; j++ {
			w := uint32(x>>(8*j)) & 0xFFFFFF
			if e := &tab[triHash(w)]; e.key == w {
				f |= e.fam
			}
		}
	}
	for ; i+2 < n; i++ {
		w := uint32(s[i]|0x20) | uint32(s[i+1]|0x20)<<8 | uint32(s[i+2]|0x20)<<16
		if e := &tab[triHash(w)]; e.key == w {
			f |= e.fam
		}
	}
	return f
}

// urlHint reports whether s may contain scheme://user:password@host.
func urlHint(s string) bool {
	i := strings.Index(s, "://")
	return i >= 0 && strings.IndexByte(s[i+3:], '@') >= 0
}
