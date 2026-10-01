package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/skill"
)

func TestPresentationObservationKeepsEvidenceWithoutPreviewBytes(t *testing.T) {
	raw := json.RawMessage(`{"rendered_previews":["data:image/png;base64,LARGE"],"scenes":[{"nodes":[]}],"catalog":{"layouts":{}},"deck_spec":{"assets":{"logo":{"data":"LARGE"}},"slides":[{"title":"用户事实"}],"style_profile":{"name":"自然科技"}},"quality_report":{"passed":true},"render_report":{"status":"verified"}}`)
	before := string(raw)
	result := publicSkillRun(skill.Run{SkillName: "office.pptx_generate", Status: "succeeded", Output: raw, Files: []skill.GeneratedFile{{Name: "overview.pptx"}}})
	encoded, _ := json.Marshal(result)
	for _, omitted := range []string{"LARGE", "rendered_previews", "scenes", "catalog", "assets"} {
		if strings.Contains(string(encoded), omitted) {
			t.Fatalf("model observation contains render payload: %s", omitted)
		}
	}
	for _, kept := range []string{"用户事实", "自然科技", "quality_report", "verified", "overview.pptx"} {
		if !strings.Contains(string(encoded), kept) {
			t.Fatalf("missing trusted evidence: %s", kept)
		}
	}
	if string(raw) != before {
		t.Fatal("workbench source output was modified")
	}
}
