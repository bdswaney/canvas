package store

import (
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

// Fold applies Unicode case folding without accent folding or normalization,
// so composed accents remain distinct from unaccented text and emoji keep
// their code-point sequences.
var searchCaseFold = cases.Fold()

func validateSearchQuery(query string) error {
	if query == "" {
		return ErrSearchQueryEmpty
	}
	if !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return ErrSearchQueryInvalid
	}
	if utf8.RuneCountInString(query) > MaxSearchQueryRunes {
		return ErrSearchQueryLong
	}
	return nil
}

func foldedSearchQuery(query string) string {
	return searchCaseFold.String(query)
}

func searchResultLess(left, right SearchResult) bool {
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.DocumentID < right.DocumentID
}

func sortSearchResults(results []SearchResult) {
	sort.Slice(results, func(i, j int) bool { return searchResultLess(results[i], results[j]) })
}
