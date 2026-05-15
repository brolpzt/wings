package server

import (
	"path"
	"path/filepath"
	"strings"
)

type fastDlPatternMatcher struct {
	patterns []string
}

func newFastDlPatternMatcher(syncPatterns string) *fastDlPatternMatcher {
	m := &fastDlPatternMatcher{}

	if syncPatterns == "" {
		return m
	}

	for _, p := range strings.Split(syncPatterns, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			m.patterns = append(m.patterns, p)
		}
	}

	return m
}

func (m *fastDlPatternMatcher) shouldSync(relPath string) bool {
	if len(m.patterns) == 0 {
		return true
	}

	relPath = filepath.ToSlash(relPath)
	base := filepath.Base(relPath)

	for _, pattern := range m.patterns {
		if strings.HasSuffix(pattern, "/") {
			dir := strings.TrimSuffix(pattern, "/")
			if relPath == dir || strings.HasPrefix(relPath, dir+"/") {
				return true
			}
			continue
		}

		if ok, _ := path.Match(pattern, relPath); ok {
			return true
		}

		if ok, _ := filepath.Match(pattern, base); ok {
			return true
		}
	}

	return false
}
