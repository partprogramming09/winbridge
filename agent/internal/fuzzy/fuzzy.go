// Package fuzzy scores how well a short text matches part of a longer one, for FocusWindow's title search.
package fuzzy

import "strings"

// PartialRatio returns 0 to 100: the best similarity between the shorter text and any equally long slice of the
// longer one, after lower-casing, like thefuzz's partial_ratio (which the Python server used).
func PartialRatio(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	short, long := []rune(a), []rune(b)
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) == 0 {
		if len(long) == 0 {
			return 100
		}
		return 0
	}
	best := 0
	for start := 0; start+len(short) <= len(long); start++ {
		if score := ratio(short, long[start:start+len(short)]); score > best {
			best = score
			if best == 100 {
				break
			}
		}
	}
	return best
}

// ratio is 100 * 2 * matches / (len(a)+len(b)) with matches taken from the longest common subsequence.
func ratio(a, b []rune) int {
	total := len(a) + len(b)
	if total == 0 {
		return 100
	}
	return 200 * lcs(a, b) / total
}

// lcs is the length of the longest common subsequence.
func lcs(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
