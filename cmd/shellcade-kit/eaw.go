package main

// East-Asian-Width classification for the wide-glyph lint.
//
// This is the SAME width judgement the host renderer's terminal makes: a code
// point counts as TWO terminal columns iff its East-Asian-Width is Wide (W) or
// Fullwidth (F). The repo has no UCD/EAW machinery to reuse (its only width
// logic, host/render, hard-codes a single Fullwidth fold and an ASCII-fallback
// table — not a general classifier), and the public SDK module is deliberately
// dependency-free, so rather than pull in golang.org/x/text/width or hand-roll a
// full UCD parser we embed the Wide+Fullwidth ranges of EastAsianWidth.txt
// directly. eaw2cols below is sorted and binary-searched; everything not in it
// is treated as a single-column class (Na/N/A/H), which is exactly the set the
// wide writers must REFUSE.
//
// Source: Unicode Character Database, EastAsianWidth.txt — the rows tagged ;W
// and ;F, coalesced into [lo,hi] ranges. Covers CJK, Hangul, fullwidth forms,
// the wide emoji blocks, and the regional/flag pictographs.

import "sort"

// eawRange is an inclusive code-point range that is EAW Wide or Fullwidth.
type eawRange struct {
	lo, hi rune
	wide   bool // true = Wide (W); false = Fullwidth (F)
}

// eaw2cols are the EAW W and F ranges — the code points a wide (width-2) writer
// may legally carry as its base. Kept sorted by lo for binary search.
var eaw2cols = []eawRange{
	{0x1100, 0x115F, true},   // Hangul Jamo
	{0x231A, 0x231B, true},   // ⌚⌛ watch/hourglass
	{0x2329, 0x232A, true},   // 〈 〉 angle brackets
	{0x23E9, 0x23EC, true},   // ⏩-⏬ media
	{0x23F0, 0x23F0, true},   // ⏰ alarm clock
	{0x23F3, 0x23F3, true},   // ⏳ hourglass
	{0x25FD, 0x25FE, true},   // ◽◾ small squares
	{0x2614, 0x2615, true},   // ☔☕
	{0x2648, 0x2653, true},   // zodiac
	{0x267F, 0x267F, true},   // ♿ wheelchair
	{0x2693, 0x2693, true},   // ⚓ anchor
	{0x26A1, 0x26A1, true},   // ⚡ high voltage
	{0x26AA, 0x26AB, true},   // ⚪⚫ circles
	{0x26BD, 0x26BE, true},   // ⚽⚾ ball
	{0x26C4, 0x26C5, true},   // ⛄⛅ snowman/cloud
	{0x26CE, 0x26CE, true},   // ⛎ ophiuchus
	{0x26D4, 0x26D4, true},   // ⛔ no entry
	{0x26EA, 0x26EA, true},   // ⛪ church
	{0x26F2, 0x26F3, true},   // ⛲⛳ fountain/flag
	{0x26F5, 0x26F5, true},   // ⛵ sailboat
	{0x26FA, 0x26FA, true},   // ⛺ tent
	{0x26FD, 0x26FD, true},   // ⛽ fuel pump
	{0x2705, 0x2705, true},   // ✅ check mark
	{0x270A, 0x270B, true},   // ✊✋ fist/hand
	{0x2728, 0x2728, true},   // ✨ sparkles
	{0x274C, 0x274C, true},   // ❌ cross mark
	{0x274E, 0x274E, true},   // ❎ negative cross
	{0x2753, 0x2755, true},   // ❓❔❕
	{0x2757, 0x2757, true},   // ❗ exclamation
	{0x2763, 0x2764, true},   // ❣❤ heart exclamation / heavy black heart
	{0x2795, 0x2797, true},   // ➕➖➗
	{0x27B0, 0x27B0, true},   // ➰ curly loop
	{0x27BF, 0x27BF, true},   // ➿ double curly loop
	{0x2B1B, 0x2B1C, true},   // ⬛⬜ large squares
	{0x2B50, 0x2B50, true},   // ⭐ star
	{0x2B55, 0x2B55, true},   // ⭕ large circle
	{0x2E80, 0x303E, true},   // CJK radicals, Kangxi, CJK symbols/punctuation
	{0x3041, 0x33FF, true},   // Hiragana..CJK compatibility
	{0x3400, 0x4DBF, true},   // CJK Ext A
	{0x4E00, 0x9FFF, true},   // CJK Unified Ideographs
	{0xA000, 0xA4CF, true},   // Yi
	{0xA960, 0xA97F, true},   // Hangul Jamo Extended-A
	{0xAC00, 0xD7A3, true},   // Hangul Syllables
	{0xF900, 0xFAFF, true},   // CJK Compatibility Ideographs
	{0xFE10, 0xFE19, true},   // Vertical forms
	{0xFE30, 0xFE6F, true},   // CJK compatibility/small forms
	{0xFF01, 0xFF60, false},  // Fullwidth Forms (incl. ７ — the pokies offender's COUSIN)
	{0xFFE0, 0xFFE6, false},  // Fullwidth signs
	{0x16FE0, 0x16FE4, true}, // Tangut/Khitan iteration marks
	{0x17000, 0x18AFF, true}, // Tangut, Khitan small script
	{0x1AFF0, 0x1B16F, true}, // Kana extensions
	{0x1F004, 0x1F004, true}, // 🀄 mahjong
	{0x1F0CF, 0x1F0CF, true}, // 🃏 joker
	{0x1F18E, 0x1F18E, true}, // 🆎
	{0x1F191, 0x1F19A, true}, // 🆑-🆚
	{0x1F200, 0x1F320, true}, // squared CJK, weather/emoji
	{0x1F32D, 0x1F335, true}, // food/plant emoji
	{0x1F337, 0x1F37C, true},
	{0x1F37E, 0x1F393, true},
	{0x1F3A0, 0x1F3CA, true},
	{0x1F3CF, 0x1F3D3, true},
	{0x1F3E0, 0x1F3F0, true},
	{0x1F3F4, 0x1F3F4, true},
	{0x1F3F8, 0x1F43E, true},
	{0x1F440, 0x1F440, true},
	{0x1F442, 0x1F4FC, true}, // many emoji incl. 💎 gem, 🔔 bell, 🍒 cherry block neighbours
	{0x1F4FF, 0x1F53D, true},
	{0x1F54B, 0x1F54E, true},
	{0x1F550, 0x1F567, true}, // clock faces
	{0x1F57A, 0x1F57A, true},
	{0x1F595, 0x1F596, true},
	{0x1F5A4, 0x1F5A4, true},
	{0x1F5FB, 0x1F64F, true}, // landmarks, faces, gestures
	{0x1F680, 0x1F6C5, true}, // transport
	{0x1F6CC, 0x1F6CC, true},
	{0x1F6D0, 0x1F6D2, true},
	{0x1F6D5, 0x1F6D7, true},
	{0x1F6DC, 0x1F6DF, true},
	{0x1F6EB, 0x1F6EC, true},
	{0x1F6F4, 0x1F6FC, true},
	{0x1F7E0, 0x1F7EB, true}, // colored circles
	{0x1F7F0, 0x1F7F0, true},
	{0x1F90C, 0x1F93A, true},
	{0x1F93C, 0x1F945, true},
	{0x1F947, 0x1F9FF, true}, // medals, food, faces, objects
	{0x1FA70, 0x1FAFF, true},
	{0x20000, 0x2FFFD, true}, // CJK Ext B..F (plane 2)
	{0x30000, 0x3FFFD, true}, // CJK Ext G (plane 3)
}

// eawClass reports a code point's relevant East-Asian-Width class and whether it
// is a two-column (Wide/Fullwidth) class. The class string is for the report:
// "W", "F", or "Na/N/A/H" for everything that occupies a single column.
func eawClass(r rune) (class string, wide bool) {
	i := sort.Search(len(eaw2cols), func(i int) bool { return eaw2cols[i].hi >= r })
	if i < len(eaw2cols) && r >= eaw2cols[i].lo && r <= eaw2cols[i].hi {
		if eaw2cols[i].wide {
			return "W", true
		}
		return "F", true
	}
	return "Na/N/A/H", false
}
