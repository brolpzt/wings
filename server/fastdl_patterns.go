package server

import (
	"path"
	"path/filepath"
	"strings"
)

type fastDlPatternMatcher struct {
	includes []string
	excludes []string
}

// parseFastDlSyncPatternLists splits the panel sync_patterns field into includes and excludes.
// A segment starting with ! (after trim) is an exclude rule; the ! is not part of the glob.
// Separators: comma, semicolon, newline.
func parseFastDlSyncPatternLists(syncPatterns string) (includes []string, excludes []string) {
	if syncPatterns == "" {
		return nil, nil
	}
	for _, part := range strings.FieldsFunc(syncPatterns, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	}) {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "!") {
			ex := strings.TrimSpace(strings.TrimPrefix(p, "!"))
			if ex != "" {
				excludes = append(excludes, ex)
			}
			continue
		}
		includes = append(includes, p)
	}
	return includes, excludes
}

func newFastDlPatternMatcher(syncPatterns string) *fastDlPatternMatcher {
	inc, exc := parseFastDlSyncPatternLists(syncPatterns)
	return &fastDlPatternMatcher{includes: inc, excludes: exc}
}

// shouldSync mirrors FastDL filter semantics (aligned with fastdl_ssh.go rsync rules):
// - Excludes win: if the path matches any exclude rule, do not sync.
// - If there are no include rules, sync everything that passed excludes.
// - Otherwise sync only if at least one include rule matches.
// Include rules: trailing / → subtree; contains / → full relative path; else basename only; ** supported.
func (m *fastDlPatternMatcher) shouldSync(relPath string) bool {
	if len(m.includes) == 0 && len(m.excludes) == 0 {
		return true
	}

	relPath = filepath.ToSlash(relPath)

	for _, raw := range m.excludes {
		if matchFastDlPattern(relPath, raw) {
			return false
		}
	}

	if len(m.includes) == 0 {
		return true
	}

	for _, raw := range m.includes {
		if matchFastDlPattern(relPath, raw) {
			return true
		}
	}

	return false
}

func matchFastDlPattern(relPath, rawPat string) bool {
	pat := strings.TrimSpace(rawPat)
	if pat == "" {
		return false
	}

	pat = filepath.ToSlash(pat)
	if strings.HasPrefix(pat, "/") {
		pat = pat[1:]
	}

	// Directory tree: "sound/" or "maps/foo/"
	if strings.HasSuffix(pat, "/") {
		dir := strings.TrimSuffix(pat, "/")
		return relPath == dir || strings.HasPrefix(relPath, dir+"/")
	}

	// Tree: "maps/**" → everything under maps/
	if strings.HasSuffix(pat, "/**") {
		prefix := strings.TrimSuffix(pat, "/**")
		prefix = strings.Trim(prefix, "/")
		if prefix == "" {
			return true
		}
		return relPath == prefix || strings.HasPrefix(relPath, prefix+"/")
	}

	if strings.Contains(pat, "**") {
		return matchDoubleStarPattern(relPath, pat)
	}

	// Full path match when pattern has a slash (rsync rule)
	if strings.Contains(pat, "/") {
		ok, _ := path.Match(pat, relPath)
		return ok
	}

	base := filepath.Base(relPath)
	ok, err := filepath.Match(pat, base)
	return err == nil && ok
}

// matchDoubleStarPattern handles a single ** segment (common FastDL cases).
// Examples: **/*.bsp, maps/**/*.wav, pre/**/foo.txt (best-effort).
func matchDoubleStarPattern(relPath, pat string) bool {
	idx := strings.Index(pat, "**")
	prefix := strings.TrimRight(pat[:idx], "/")
	suffix := strings.TrimLeft(pat[idx+2:], "/")

	if prefix != "" && relPath != prefix && !strings.HasPrefix(relPath, prefix+"/") {
		return false
	}

	var rest string
	if prefix == "" {
		rest = relPath
	} else {
		rest = relPath[len(prefix)+1:]
	}

	if suffix == "" {
		return true
	}

	if strings.Contains(suffix, "**") {
		ok, _ := path.Match(strings.ReplaceAll(suffix, "**", "*"), rest)
		return ok
	}

	if strings.Contains(suffix, "/") {
		ok, _ := path.Match(suffix, rest)
		return ok
	}

	base := filepath.Base(rest)
	if rest == "" {
		base = filepath.Base(relPath)
	}
	ok, err := filepath.Match(suffix, base)
	return err == nil && ok
}
