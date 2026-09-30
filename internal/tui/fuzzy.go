package tui

import (
	"sort"
	"strings"
)

// fuzzyRank keeps the items whose hay matches q as a subsequence, best
// first. An empty q keeps every item in order.
func fuzzyRank[T any](items []T, q string, hay func(T) string) []T {
	type scored struct {
		item  T
		score int
	}
	var ms []scored
	for _, it := range items {
		if s, ok := fuzzyScore(q, hay(it)); ok {
			ms = append(ms, scored{it, s})
		}
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].score > ms[j].score })
	out := make([]T, len(ms))
	for i, m := range ms {
		out[i] = m.item
	}
	return out
}

// fuzzyScore rewards consecutive runs and word starts so the tightest
// match sorts first.
func fuzzyScore(pattern, text string) (int, bool) {
	if pattern == "" {
		return 0, true
	}
	p, t := strings.ToLower(pattern), strings.ToLower(text)
	score, ti, last := 0, 0, -2
	for pi := 0; pi < len(p); pi++ {
		c := p[pi]
		if c == ' ' {
			continue
		}
		found := false
		for ; ti < len(t); ti++ {
			if t[ti] != c {
				continue
			}
			score++
			if ti == last+1 {
				score += 6
			}
			if ti == 0 || strings.IndexByte(" -_./:", t[ti-1]) >= 0 {
				score += 10
			}
			last = ti
			ti++
			found = true
			break
		}
		if !found {
			return 0, false
		}
	}
	return score - len(t)/12, true
}
