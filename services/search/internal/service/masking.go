package service

// QA SD-22: search snippets printed unmasked card numbers and SSNs even
// though the PII pipeline had already flagged the document Critical — the
// snippet path bypassed the redaction surfaces entirely. MaskSensitive
// redacts the two pattern classes with real regulatory teeth on every
// string mapHit emits:
//
//   - payment cards: 13–19 digits (spaces/dashes allowed) that pass the
//     Luhn check. PCI DSS permits showing at most the first six and last
//     four, so the middle digits become '*' in place (separators kept).
//   - SSNs in the canonical ddd-dd-dddd form: first five digits masked,
//     last four kept ("***-**-6789").
//
// OpenSearch highlight fragments interleave <em>…</em> markers inside the
// text, so matching runs on the tag-stripped shadow of the string and the
// masking is mapped back onto the original bytes — tags survive, digits
// under them don't.

// visibleText returns the string with anything inside <...> removed, plus
// a map from each visible-byte index back to its index in the original.
func visibleText(s string) (string, []int) {
	out := make([]byte, 0, len(s))
	idx := make([]int, 0, len(s))
	inTag := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '<':
			inTag = true
		case s[i] == '>' && inTag:
			inTag = false
		case !inTag:
			out = append(out, s[i])
			idx = append(idx, i)
		}
	}
	return string(out), idx
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// luhnValid reports whether digits (ASCII) pass the Luhn checksum.
func luhnValid(digits []byte) bool {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// MaskSensitive redacts card numbers and SSNs in s, preserving layout
// (separators and any <tags> stay where they were).
func MaskSensitive(s string) string {
	vis, backmap := visibleText(s)
	if len(vis) == 0 {
		return s
	}
	masked := []byte(s)
	changed := false

	// ---- SSN: ddd-dd-dddd with non-digit boundaries ----
	for i := 0; i+11 <= len(vis); i++ {
		if i > 0 && (isDigit(vis[i-1]) || vis[i-1] == '-') {
			continue
		}
		if !(isDigit(vis[i]) && isDigit(vis[i+1]) && isDigit(vis[i+2]) && vis[i+3] == '-' &&
			isDigit(vis[i+4]) && isDigit(vis[i+5]) && vis[i+6] == '-' &&
			isDigit(vis[i+7]) && isDigit(vis[i+8]) && isDigit(vis[i+9]) && isDigit(vis[i+10])) {
			continue
		}
		if i+11 < len(vis) && (isDigit(vis[i+11]) || vis[i+11] == '-') {
			continue
		}
		for _, off := range []int{0, 1, 2, 4, 5} { // mask the first five digits
			masked[backmap[i+off]] = '*'
			changed = true
		}
	}

	// ---- payment cards: 13–19 digits, single space/dash separators ----
	for i := 0; i < len(vis); i++ {
		if !isDigit(vis[i]) {
			continue
		}
		if i > 0 && isDigit(vis[i-1]) {
			continue // mid-run; runs are consumed from their first digit
		}
		// Collect a run of digits allowing single ' ' or '-' separators.
		digits := make([]byte, 0, 19)
		positions := make([]int, 0, 19)
		j := i
		for j < len(vis) {
			if isDigit(vis[j]) {
				digits = append(digits, vis[j])
				positions = append(positions, j)
				j++
				continue
			}
			if (vis[j] == ' ' || vis[j] == '-') && j+1 < len(vis) && isDigit(vis[j+1]) && len(digits) > 0 {
				j++
				continue
			}
			break
		}
		if len(digits) >= 13 && len(digits) <= 19 && luhnValid(digits) {
			for k := 6; k < len(digits)-4; k++ { // PCI: first six + last four may remain
				masked[backmap[positions[k]]] = '*'
				changed = true
			}
		}
		if j > i {
			i = j - 1 // resume after the run (loop's i++ moves past it)
		}
	}

	if !changed {
		return s
	}
	return string(masked)
}
