package store

import (
	"sort"
	"strings"
)

// SortFacts sorts a fact slice by key in ascending order. Required before
// calling FactsWithPrefix or FindPrefixRange.
func SortFacts(facts []Fact) {
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].Key < facts[j].Key
	})
}

// FindPrefixRange returns the start and end indices of facts matching the
// given prefix in a sorted fact slice. Uses binary search to find the first
// match in O(log n), then walks forward to find the end of the prefix range.
// Returns (0, 0) if no facts match.
func FindPrefixRange(facts []Fact, prefix string) (int, int) {
	if len(facts) == 0 || prefix == "" {
		return 0, 0
	}

	startIndex := sort.Search(len(facts), func(index int) bool {
		return facts[index].Key >= prefix
	})

	if startIndex >= len(facts) || !strings.HasPrefix(facts[startIndex].Key, prefix) {
		return 0, 0
	}

	endIndex := startIndex
	for endIndex < len(facts) && strings.HasPrefix(facts[endIndex].Key, prefix) {
		endIndex++
	}

	return startIndex, endIndex
}

// FactsWithPrefix returns the sub-slice of facts matching the prefix using
// binary search. The input slice must be sorted by key. Returns nil if no
// facts match.
func FactsWithPrefix(facts []Fact, prefix string) []Fact {
	startIndex, endIndex := FindPrefixRange(facts, prefix)
	if startIndex == endIndex {
		return nil
	}
	return facts[startIndex:endIndex]
}
