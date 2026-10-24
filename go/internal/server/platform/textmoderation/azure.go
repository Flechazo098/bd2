package textmoderation

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

var azureCategories = [...]string{"Hate", "Sexual", "SelfHarm", "Violence"}

// https://learn.microsoft.com/en-us/rest/api/contentsafety/text-operations/analyze-text?view=rest-contentsafety-2024-09-01
func azureRequest(text string) ([]byte, error) {
	if utf8.RuneCountInString(text) > 10000 {
		return nil, fmt.Errorf("%w: text is longer than 10000 characters", ErrUnavailable)
	}
	return json.Marshal(struct {
		Text       string    `json:"text"`
		Categories [4]string `json:"categories"`
		OutputType string    `json:"outputType"`
	}{text, azureCategories, "EightSeverityLevels"})
}

func azureResult(data []byte, threshold int) error {
	var result struct {
		CategoriesAnalysis []struct {
			Category string `json:"category"`
			Severity *int   `json:"severity"`
		} `json:"categoriesAnalysis"`
		BlocklistsMatch []json.RawMessage `json:"blocklistsMatch"`
	}
	if !decodeResponse(data, &result, false) || len(result.CategoriesAnalysis) != len(azureCategories) {
		return fmt.Errorf("%w: Azure response is malformed or has %d categories, want %d", ErrUnavailable, len(result.CategoriesAnalysis), len(azureCategories))
	}
	seen := map[string]bool{}
	reject := len(result.BlocklistsMatch) > 0
	for _, item := range result.CategoriesAnalysis {
		known := false
		for _, category := range azureCategories {
			if item.Category == category {
				known = true
				break
			}
		}
		if !known || seen[item.Category] || item.Severity == nil || *item.Severity < 0 || *item.Severity > 7 {
			return fmt.Errorf("%w: Azure category %q is unknown, repeated or has an invalid severity", ErrUnavailable, item.Category)
		}
		seen[item.Category] = true
		reject = reject || *item.Severity >= threshold
	}
	if reject {
		return fmt.Errorf("%w: Azure Content Safety", ErrRejected)
	}
	return nil
}
