package git

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Anchor identifies a changed line on the old or new side of an immutable diff.
type Anchor struct {
	Path string
	Line int
	Side string
}

var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

func changedAnchors(f File) ([]Anchor, error) {
	if f.Binary {
		return nil, nil
	}
	out := []Anchor{}
	oldLine, newLine := 0, 0
	inHunk := false
	for _, line := range strings.Split(f.Patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("invalid Git hunk header")
			}
			var e error
			oldLine, e = strconv.Atoi(m[1])
			if e != nil {
				return nil, e
			}
			newLine, e = strconv.Atoi(m[2])
			if e != nil {
				return nil, e
			}
			inHunk = true
			continue
		}
		if !inHunk || line == "" || strings.HasPrefix(line, "\\") {
			continue
		}
		switch line[0] {
		case '-':
			if f.OldPath != "" {
				out = append(out, Anchor{f.OldPath, oldLine, "old"})
			}
			oldLine++
		case '+':
			if f.NewPath != "" {
				out = append(out, Anchor{f.NewPath, newLine, "new"})
			}
			newLine++
		case ' ':
			oldLine++
			newLine++
		default:
			inHunk = false
		}
	}
	return out, nil
}
