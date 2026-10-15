package snapshot

// A port of git's wildmatch.c (the matcher behind pathspecs and gitignore
// style patterns), so that exempt filtering and `git rm --cached --
// ':(glob)<pattern>'` agree on every input. Only the ASCII/case-sensitive
// subset git uses for pathspecs is implemented (no WM_CASEFOLD).

const (
	wmMatch           = 0
	wmNoMatch         = 1
	wmAbortAll        = -1
	wmAbortToStarStar = -2
)

// wildmatch reports whether text matches pattern. With pathname set, '*' and
// '?' do not match '/' (WM_PATHNAME, what ':(glob)' uses); otherwise '*'
// matches '/' too (plain pathspecs).
func wildmatch(pattern, text string, pathname bool) bool {
	return dowild(pattern, 0, text, 0, pathname) == wmMatch
}

func byteAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func isGlobSpecial(c byte) bool {
	return c == '*' || c == '?' || c == '[' || c == '\\'
}

func dowild(pat string, p int, txt string, t int, pathname bool) int {
	for ; p < len(pat); t, p = t+1, p+1 {
		tCh := byteAt(txt, t)
		pCh := pat[p]
		if tCh == 0 && t >= len(txt) && pCh != '*' {
			return wmAbortAll
		}
		switch pCh {
		case '\\':
			p++
			pCh = byteAt(pat, p)
			if tCh != pCh || t >= len(txt) {
				return wmNoMatch
			}
		case '?':
			if pathname && tCh == '/' {
				return wmNoMatch
			}
		case '*':
			p++
			var matchSlash bool
			if byteAt(pat, p) == '*' {
				prev := p - 2
				for p++; byteAt(pat, p) == '*'; p++ {
				}
				switch {
				case !pathname:
					// Without WM_PATHNAME, "**" is just "*".
					matchSlash = true
				case (prev < 0 || pat[prev] == '/') &&
					(p >= len(pat) || pat[p] == '/' || (pat[p] == '\\' && byteAt(pat, p+1) == '/')):
					// "<star><star>/" matches zero directories as well.
					if p < len(pat) && pat[p] == '/' && dowild(pat, p+1, txt, t, pathname) == wmMatch {
						return wmMatch
					}
					matchSlash = true
				default:
					matchSlash = false
				}
			} else {
				matchSlash = !pathname
			}
			if p >= len(pat) {
				if !matchSlash {
					for i := t; i < len(txt); i++ {
						if txt[i] == '/' {
							return wmNoMatch
						}
					}
				}
				return wmMatch
			} else if !matchSlash && pat[p] == '/' {
				// One star followed by a slash matches up to the next slash.
				slash := -1
				for i := t; i < len(txt); i++ {
					if txt[i] == '/' {
						slash = i
						break
					}
				}
				if slash < 0 {
					return wmNoMatch
				}
				t = slash
				// The slash is consumed by the loop post statement.
				continue
			}
			for {
				tCh = byteAt(txt, t)
				if t >= len(txt) {
					break
				}
				if !isGlobSpecial(pat[p]) {
					pc := pat[p]
					for t < len(txt) && (matchSlash || txt[t] != '/') && txt[t] != pc {
						t++
					}
					if t >= len(txt) || txt[t] != pc {
						return wmNoMatch
					}
					tCh = txt[t]
				}
				matched := dowild(pat, p, txt, t, pathname)
				if matched != wmNoMatch {
					if !matchSlash || matched != wmAbortToStarStar {
						return matched
					}
				} else if !matchSlash && tCh == '/' {
					return wmAbortToStarStar
				}
				t++
			}
			return wmAbortAll
		case '[':
			p++
			pCh = byteAt(pat, p)
			if pCh == '^' {
				pCh = '!'
			}
			negated := pCh == '!'
			if negated {
				p++
				pCh = byteAt(pat, p)
			}
			var prevCh byte
			matched := false
			for {
				if p >= len(pat) {
					return wmAbortAll
				}
				switch {
				case pCh == '\\':
					p++
					if p >= len(pat) {
						return wmAbortAll
					}
					pCh = pat[p]
					if tCh == pCh {
						matched = true
					}
				case pCh == '-' && prevCh != 0 && byteAt(pat, p+1) != 0 && byteAt(pat, p+1) != ']':
					p++
					pCh = pat[p]
					if pCh == '\\' {
						p++
						if p >= len(pat) {
							return wmAbortAll
						}
						pCh = pat[p]
					}
					if tCh <= pCh && tCh >= prevCh {
						matched = true
					}
					pCh = 0 // makes prevCh 0 below
				case pCh == '[' && byteAt(pat, p+1) == ':':
					p += 2
					s := p
					for p < len(pat) && pat[p] != ']' {
						p++
					}
					if p >= len(pat) {
						return wmAbortAll
					}
					i := p - s - 1
					if i < 0 || pat[p-1] != ':' {
						// No ":]": treat the '[' as an ordinary member.
						p = s - 2
						pCh = '['
						if tCh == pCh {
							matched = true
						}
						break
					}
					ok, valid := charClass(pat[s:s+i], tCh)
					if !valid {
						return wmAbortAll
					}
					if ok {
						matched = true
					}
					pCh = 0
				default:
					if tCh == pCh {
						matched = true
					}
				}
				prevCh = pCh
				p++
				pCh = byteAt(pat, p)
				if p >= len(pat) {
					return wmAbortAll
				}
				if pCh == ']' {
					break
				}
			}
			if matched == negated || (pathname && tCh == '/') {
				return wmNoMatch
			}
		default:
			if tCh != pCh || t >= len(txt) {
				return wmNoMatch
			}
		}
	}
	if t < len(txt) {
		return wmNoMatch
	}
	return wmMatch
}

// charClass evaluates a POSIX bracket class name for c; valid is false for an
// unknown class name.
func charClass(name string, c byte) (ok, valid bool) {
	lower := c >= 'a' && c <= 'z'
	upper := c >= 'A' && c <= 'Z'
	digit := c >= '0' && c <= '9'
	switch name {
	case "alnum":
		return lower || upper || digit, true
	case "alpha":
		return lower || upper, true
	case "blank":
		return c == ' ' || c == '\t', true
	case "cntrl":
		return c < 0x20 || c == 0x7f, true
	case "digit":
		return digit, true
	case "graph":
		return c > 0x20 && c < 0x7f, true
	case "lower":
		return lower, true
	case "print":
		return c >= 0x20 && c < 0x7f, true
	case "punct":
		return c > 0x20 && c < 0x7f && !(lower || upper || digit), true
	case "space":
		return c == ' ' || (c >= '\t' && c <= '\r'), true
	case "upper":
		return upper, true
	case "xdigit":
		return digit || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'), true
	}
	return false, false
}
