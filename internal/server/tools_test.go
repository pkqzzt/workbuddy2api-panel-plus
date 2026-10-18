package server

import (
	"encoding/json"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestBuildToolsSearchResponseMatchesDeepSeekContract 逐条对齐 DSH 的校验契约。
//
// 客户端 mapSearchResponse 要求：results 必须是数组；每项 url 非空字符串、
// title 与 snippet 必须是 string（不能缺失）、date 必须显式为 string 或 null。
// 任一不满足都会被判成 WEB_PROVIDER_ERROR，整次搜索失败。
func TestBuildToolsSearchResponseMatchesDeepSeekContract(t *testing.T) {
	out := &upstream.SearchOutcome{Results: []upstream.SearchResult{
		{Title: "标题一", URL: "https://example.com/1", Snippet: "摘要一"},
		{Title: "", URL: "https://example.com/2", Snippet: ""},
	}}
	raw, err := json.Marshal(buildToolsSearchResponse(out))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	var got struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("results 长度 = %d, want 2", len(got.Results))
	}
	for i, item := range got.Results {
		u, ok := item["url"].(string)
		if !ok || u == "" {
			t.Errorf("item[%d]: url 必须是非空 string，得到 %v", i, item["url"])
		}
		if _, ok := item["title"].(string); !ok {
			t.Errorf("item[%d]: title 必须是 string，得到 %v", i, item["title"])
		}
		if _, ok := item["snippet"].(string); !ok {
			t.Errorf("item[%d]: snippet 必须是 string，得到 %v", i, item["snippet"])
		}
		date, exists := item["date"]
		if !exists {
			t.Errorf("item[%d]: date 字段必须存在（可为 null）", i)
		}
		if date != nil {
			t.Errorf("item[%d]: date 应为 null，得到 %v", i, date)
		}
	}
	// 无标题时用 URL 兜底，避免客户端渲染出空白条目。
	if got.Results[1]["title"] != "https://example.com/2" {
		t.Errorf("空标题未回退为 URL，得到 %v", got.Results[1]["title"])
	}
}

// TestBuildToolsSearchResponseEmptyIsArray 空结果必须序列化成 []，不能是 null。
func TestBuildToolsSearchResponseEmptyIsArray(t *testing.T) {
	raw, err := json.Marshal(buildToolsSearchResponse(&upstream.SearchOutcome{}))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(raw) != `{"results":[]}` {
		t.Errorf("空结果应序列化为 {\"results\":[]}，得到 %s", raw)
	}
}
