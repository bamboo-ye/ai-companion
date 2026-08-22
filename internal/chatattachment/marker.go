package chatattachment

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	documentPattern         = regexp.MustCompile(`<!--ai-document:([0-9a-fA-F-]{36})(?:\|([^>]*))?-->`)
	internalMetadataPattern = regexp.MustCompile(`(?s)<!--\s*ai-(?:document|generated-file|ledger-export|skill-run|agent-run|generation-job):.*?-->`)
)

func AppendDocument(content, documentID, name string) string {
	documentID = strings.TrimSpace(documentID)
	if documentID == "" {
		return strings.TrimSpace(content)
	}
	name = strings.NewReplacer("\n", " ", "\r", " ", "|", "-").Replace(strings.TrimSpace(name))
	return strings.TrimSpace(content) + "\n<!--ai-document:" + documentID + "|" + name + "-->"
}

func DocumentIDs(content string) []string {
	matches := documentPattern.FindAllStringSubmatch(content, -1)
	result := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) >= 2 && !seen[match[1]] {
			seen[match[1]] = true
			result = append(result, match[1])
		}
	}
	return result
}

func VisibleText(content string) string {
	return strings.TrimSpace(internalMetadataPattern.ReplaceAllString(content, ""))
}

func GeneratedFileMarker(runID, fileID, name string) string {
	name = strings.NewReplacer("\n", " ", "\r", " ", "|", "-").Replace(strings.TrimSpace(name))
	return "<!--ai-generated-file:" + runID + "|" + fileID + "|" + name + "-->"
}

func SkillRunMarker(runID string, attempt int, status string) string {
	runID = strings.TrimSpace(runID)
	status = strings.NewReplacer("\n", "", "\r", "", "|", "-").Replace(strings.TrimSpace(status))
	if runID == "" || attempt < 1 || status == "" {
		return ""
	}
	return "<!--ai-skill-run:" + runID + "|" + fmt.Sprint(attempt) + "|" + status + "-->"
}

func LedgerExportMarker(exportID, name string) string {
	name = strings.NewReplacer("\n", " ", "\r", " ", "|", "-").Replace(strings.TrimSpace(name))
	return "<!--ai-ledger-export:" + strings.TrimSpace(exportID) + "|" + name + "-->"
}
