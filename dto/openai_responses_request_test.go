package dto

import (
	"encoding/json"
	"testing"
)

func TestOpenAIResponsesRequest_HasImageGenerationTool(t *testing.T) {
	cases := []struct {
		name     string
		toolsRaw string
		want     bool
	}{
		{name: "nil tools", toolsRaw: ``, want: false},
		{name: "empty array", toolsRaw: `[]`, want: false},
		{
			name:     "single image_generation",
			toolsRaw: `[{"type":"image_generation"}]`,
			want:     true,
		},
		{
			name:     "image_generation among many tools",
			toolsRaw: `[{"type":"web_search"},{"type":"image_generation","model":"gpt-image-2"},{"type":"file_search"}]`,
			want:     true,
		},
		{
			name:     "no image_generation tools",
			toolsRaw: `[{"type":"web_search"},{"type":"file_search"}]`,
			want:     false,
		},
		{
			name:     "non-string type field is ignored",
			toolsRaw: `[{"type":1}]`,
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := OpenAIResponsesRequest{}
			if tc.toolsRaw != "" {
				req.Tools = json.RawMessage(tc.toolsRaw)
			}
			if got := req.HasImageGenerationTool(); got != tc.want {
				t.Fatalf("HasImageGenerationTool(%s) = %v, want %v", tc.toolsRaw, got, tc.want)
			}
		})
	}
}
