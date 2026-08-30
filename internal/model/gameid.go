package model

import (
	"regexp"
	"strings"
)

// Game identifiers appear in three shapes in the wild:
//
//	SYSTEM.CNF / OPL / hdl_dump   SLUS_209.46
//	filenames, cover databases    SLUS-20946
//	sloppy user input             slus20946
//
// NormalizeGameID collapses all of them to a comparison key; OPLGameID and
// DashedGameID render the two canonical forms back out. Everything that
// compares identities goes through NormalizeGameID so the three shapes never
// split one title into two catalog entries.

var (
	// A serial is a run of letters followed by a run of digits, optionally
	// separated by the usual punctuation. Four letters and five digits is by
	// far the most common shape but neither is guaranteed, so the pattern
	// stays permissive and the formatters degrade gracefully.
	serialRe = regexp.MustCompile(`(?i)\b([A-Z]{2,5})[ _\-]?([0-9]{3})[ .\-]?([0-9]{2})\b`)

	nonAlnumRe = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// NormalizeGameID strips punctuation and upper-cases, yielding the key used
// for equality. It returns "" for input that holds no alphanumerics.
func NormalizeGameID(id string) string {
	s := nonAlnumRe.ReplaceAllString(id, "")
	return strings.ToUpper(s)
}

// splitSerial breaks a normalised serial into its letter and digit runs.
// ok is false when the value does not look like a serial at all.
func splitSerial(norm string) (letters, digits string, ok bool) {
	i := 0
	for i < len(norm) && norm[i] >= 'A' && norm[i] <= 'Z' {
		i++
	}
	if i == 0 || i == len(norm) {
		return "", "", false
	}
	letters, digits = norm[:i], norm[i:]
	for j := 0; j < len(digits); j++ {
		if digits[j] < '0' || digits[j] > '9' {
			return "", "", false
		}
	}
	return letters, digits, true
}

// OPLGameID renders the SLUS_209.46 form used by SYSTEM.CNF, hdl_dump, OPL
// artwork filenames and OPL per-game CFG files. Input that cannot be parsed as
// a serial is returned upper-cased and otherwise untouched.
func OPLGameID(id string) string {
	norm := NormalizeGameID(id)
	letters, digits, ok := splitSerial(norm)
	if !ok || len(digits) < 3 {
		return strings.ToUpper(strings.TrimSpace(id))
	}
	// The dot always sits two digits from the end; the underscore follows the
	// letters. Serials with more or fewer than five digits keep their length.
	return letters + "_" + digits[:len(digits)-2] + "." + digits[len(digits)-2:]
}

// DashedGameID renders the SLUS-20946 form used by PCSX2, DuckStation and the
// community cover databases those two consume.
func DashedGameID(id string) string {
	norm := NormalizeGameID(id)
	letters, digits, ok := splitSerial(norm)
	if !ok {
		return strings.ToUpper(strings.TrimSpace(id))
	}
	return letters + "-" + digits
}

// FindGameID extracts the first serial-shaped token from arbitrary text such
// as a BOOT2 line, a filename or a partition name. It returns "" when the text
// contains nothing serial-shaped.
func FindGameID(s string) string {
	m := serialRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return OPLGameID(m[1] + m[2] + m[3])
}

// bootFileName matches a boot file exactly as a disc's root directory writes
// it: four letters, an underscore, three digits, a dot, two digits, and the
// ISO 9660 version suffix.
//
// FindGameID is deliberately looser -- it has to find a serial inside a name
// somebody typed -- and that looseness is wrong against a machine-written
// directory listing, where it also matches data files.
var bootFileName = regexp.MustCompile(`^([A-Z]{4})_([0-9]{3})\.([0-9]{2})(;[0-9]+)?$`)

// BootFileSerial returns the serial a root-directory entry names, or "".
//
// Both consoles put it there: a PS2 disc carries SLUS_202.16;1 as its boot
// ELF, a PS1 disc carries SLUS_000.01;1 next to SYSTEM.CNF. It is what makes a
// disc identifiable when SYSTEM.CNF itself is out of reach.
func BootFileSerial(name string) string {
	m := bootFileName.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(name)))
	if m == nil {
		return ""
	}
	return OPLGameID(m[1] + m[2] + m[3])
}
