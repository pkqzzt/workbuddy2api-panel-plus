// fetch.go DeepSeek Fetch API 兼容端点（POST /v1/tools/fetch）。
//
// DSH 的 deepseek-fetch-api provider 直连 {baseURL}/fetch：请求 {url}，响应必须是
// {"content":"<markdown>"}（客户端校验 isRecord(payload) && typeof payload.content
// === "string"，否则判 invalid response）。非 2xx 一律视为抓取失败。
//
// 这个端点等于「让网关代取任意 URL」，是标准的 SSRF 面，因此约束比 search 严格：
//   - 只接受 http/https；
//   - 解析域名后逐个检查 IP，命中回环 / 私网 / 链路本地 / 云元数据（169.254.169.254）
//     一律拒绝；
//   - 重定向自己接管并逐跳复检（默认客户端会直接跟 Location，绕过入口校验）；
//   - 响应体与正文都设上限，只接受文本类 Content-Type。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	converter "github.com/JohannesKaufmann/html-to-markdown"
)

const (
	fetchMaxBodyBytes = 4 << 20 // 原始响应体上限 4 MiB
	fetchMaxChars     = 200_000 // 回给客户端的正文上限（字符）
	fetchTimeout      = 20 * time.Second
	fetchMaxRedirects = 3
)

// errFetchBlocked 表示目标地址被安全策略拒绝。
var errFetchBlocked = errors.New("target address is not allowed")

// isPublicIP 判断 IP 是否为可访问的公网地址。
//
// 覆盖 net.IP 自带的回环/私网/链路本地/多播判定，另补 CGNAT(100.64/10) 与 IPv6 ULA。
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 运营商级 NAT，常见于容器/云内网。
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		return true
	}
	// IPv6 唯一本地地址 fc00::/7。
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return true
}

// validateFetchTarget 校验目标 URL：协议白名单 + 解析后逐 IP 公网校验。
func validateFetchTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http/https is allowed, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("url has no host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, errFetchBlocked
		}
		return u, nil
	}
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip", host)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve %s", host)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, errFetchBlocked
		}
	}
	return u, nil
}

// isTextualContentType 判断是否为可转换的文本类响应。
func isTextualContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") ||
		strings.Contains(ct, "application/xhtml") ||
		strings.Contains(ct, "text/plain") ||
		strings.Contains(ct, "text/markdown")
}

// truncateRunes 按字符（而非字节）截断，避免切坏多字节序列。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// htmlToMarkdown 把 HTML 正文转换成 Markdown。
func htmlToMarkdown(page string) (string, error) {
	out, err := converter.NewConverter("", true, nil).ConvertString(page)
	if err != nil {
		return "", fmt.Errorf("convert html: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", errors.New("page has no readable content")
	}
	return truncateRunes(out, fetchMaxChars), nil
}

// fetchPage 抓取目标页面并转成 Markdown。
func fetchPage(ctx context.Context, target *url.URL) (string, error) {
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= fetchMaxRedirects {
				return fmt.Errorf("too many redirects (>%d)", fetchMaxRedirects)
			}
			// 逐跳复检：否则 https://public.example 可以 302 到 127.0.0.1 绕过入口校验。
			if _, err := validateFetchTarget(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; workbuddy2api-fetch/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("target returned HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !isTextualContentType(ct) {
		return "", fmt.Errorf("unsupported content-type %q", ct)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBodyBytes))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", errors.New("target returned an empty body")
	}
	if strings.Contains(strings.ToLower(ct), "html") {
		return htmlToMarkdown(text)
	}
	return truncateRunes(text, fetchMaxChars), nil
}

// toolsFetch 处理 POST /v1/tools/fetch。
func (h *Handler) toolsFetch(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": `request body must be JSON like {"url":"https://..."}`,
		})
		return
	}
	target, verr := validateFetchTarget(req.URL)
	if verr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": verr.Error()})
		return
	}
	text, ferr := fetchPage(r.Context(), target)
	if ferr != nil {
		// 502：网关侧抓取失败（客户端据此判定抓取未成功，与目标页面状态无关）。
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": ferr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": text})
}
