package server

import "testing"

// TestIsSearchPathCoversAllToolEndpoints 三个工具端点都必须纳入请求日志记账。
//
// 曾漏掉 /v1/tools/*：DSH 走 DeepSeek Search/Fetch API 形态时，面板「请求记录」
// 里看不到任何流量，排查"到底有没有走网关"时反而成了盲区。
func TestIsSearchPathCoversAllToolEndpoints(t *testing.T) {
	for _, p := range []string{"/v1/search/messages", "/v1/tools/search", "/v1/tools/fetch"} {
		if !isSearchPath(p) {
			t.Errorf("isSearchPath(%q) = false，应记账", p)
		}
	}
	for _, p := range []string{"/v1/chat/completions", "/v1/models", "/status", "/healthz", "/v1/tools/other"} {
		if isSearchPath(p) {
			t.Errorf("isSearchPath(%q) = true，不该走搜索记账分支", p)
		}
	}
}
