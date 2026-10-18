// tools.go DeepSeek Search API 兼容端点（POST /v1/tools/search）。
//
// 背景：DSH 桌面端的内置搜索插件 @deepseek-ai/dsh-web-deepseek-api 不认 Anthropic
// Messages 形态——它按 DeepSeek 官方 Search API 直连 {baseURL}/search，请求体
// {query, count, need_content, ori_question}，并严格校验响应
// {results:[{url,title,snippet,date}]}：url 非空、title 与 snippet 必须是 string、
// date 必须显式为 string 或 null。missing.go 的 /v1/search/messages 因此接不上它
// （路径与协议都不同，实测 DSH 请求 {baseURL}/search 得到 404）。
//
// 本文件只做协议翻译：选号 / 轮转 / 在途租约 / token 预刷新 / 错误策略全部复用
// search.go 的 runSearch，与 chat 和 Messages 形态共享同一套账号健康语义。
package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// toolsSearchRequest 是 DeepSeek Search API 的请求体。
// need_content 与 ori_question 由客户端按官方契约发送，本网关不透传：上游
// /agenttool/v1/search 不接受这两个字段，且正文抓取由 DSH 的 Fetch provider 负责。
type toolsSearchRequest struct {
	Query        string `json:"query"`
	Count        int    `json:"count"`
	NeedContent  bool   `json:"need_content"`
	OriginalText string `json:"ori_question"`
}

// toolsSearchItem 对齐 DSH 的逐项校验。date 用 *string 以保证序列化出显式
// null（缺字段会被客户端判成 invalid response）；上游不提供发布时间，恒为 null。
type toolsSearchItem struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Snippet string  `json:"snippet"`
	Date    *string `json:"date"`
}

// toolsSearchResponse 是 Search API 的成功响应。
type toolsSearchResponse struct {
	Results []toolsSearchItem `json:"results"`
}

// buildToolsSearchResponse 把上游结果投影成 DeepSeek Search API 形态。
//
// 纯函数，便于单测逐条验证 DSH 的校验契约（见 tools_test.go）。
// 空结果返回空数组而非 null：客户端要求 results 必须是数组。
func buildToolsSearchResponse(out *upstream.SearchOutcome) toolsSearchResponse {
	resp := toolsSearchResponse{Results: make([]toolsSearchItem, 0, len(out.Results))}
	for _, item := range out.Results {
		title := item.Title
		if title == "" {
			// 无标题时用 URL 兜底：客户端对空 title 虽不判错，但渲染会空白。
			title = item.URL
		}
		resp.Results = append(resp.Results, toolsSearchItem{
			Title:   title,
			URL:     item.URL,
			Snippet: item.Snippet,
			Date:    nil,
		})
	}
	return resp
}

// toolsSearch 处理 POST /v1/tools/search。
func (h *Handler) toolsSearch(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	var req toolsSearchRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": `request body must be JSON like {"query":"..."}`,
		})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query is required"})
		return
	}
	maxResults := req.Count
	if maxResults <= 0 {
		maxResults = upstream.SearchDefaultMaxResults
	}
	out, fail := h.runSearch(r.Context(), query, maxResults, searchMetaFromRequest(r))
	if fail != nil {
		writeJSON(w, fail.status, map[string]string{"error": fail.msg})
		return
	}
	writeJSON(w, http.StatusOK, buildToolsSearchResponse(out))
}
