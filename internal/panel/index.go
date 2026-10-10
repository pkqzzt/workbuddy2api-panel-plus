// index.go 面板静态资源与安全响应头。
//
// 资源经 go:embed 打进二进制（随服务部署，无外部构建步骤）：
//   - index.html  页面骨架
//   - app.js      全部前端逻辑（独立文件而非内联，为了启用无需 unsafe-inline 的严格 CSP）
//   - mobile.css  手机端（≤760px）适配样式（本仓库自有，上游不维护 → 同步上游不冲突）
//   - mobile.js   手机端联动逻辑（抽屉开合、宽表卡片化字段名），须在 app.js 之后加载
//
// 安全头对"面板页面与全部 /panel/api/* 响应"统一生效：CSP 限制脚本只能来自本服务，
// 禁止被 iframe 嵌套（防点击劫持），禁 MIME 嗅探，并声明不泄露 Referer 出去。
package panel

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strings"
)

//go:embed index.html
var indexHTML []byte

//go:embed app.js
var appJS []byte

//go:embed mobile.css
var mobileCSS []byte

//go:embed mobile.js
var mobileJS []byte

// csp 内容安全策略（严格版，无需 unsafe-inline）：
//   - default-src 'none'        默认全禁，逐个开口
//   - script-src 'self'         只跑同源脚本（app.js / mobile.js）；页面无内联事件处理器/内联脚本
//   - style-src 'self' 'unsafe-inline'
//     style 的内联是设计取舍：页面有少量 style="..." 属性（进度条宽度、表格列宽），
//     允许内联样式不会导致脚本执行；仍禁止外部样式域与 @import 外链。
//   - connect-src 'self'        前端 fetch 只能打本服务
//   - img-src 'self' data:      图标/内联图
//   - form-action 'none'        页面无表单提交目标（配置页是 JS 提交）
//   - frame-ancestors 'none'    禁止被任何站点 iframe 嵌套（点击劫持）
//   - base-uri 'none'          禁止注入 <base> 改写相对路径
const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; img-src 'self' data:; form-action 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// setSecurityHeaders 写入面板统一安全响应头（页面与 API 都要，API 也含 JSON 数据）。
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff") // 禁 MIME 嗅探
	w.Header().Set("X-Frame-Options", "DENY")           // 老浏览器兜底（CSP frame-ancestors 的等价项）
	w.Header().Set("Referrer-Policy", "no-referrer")    // 不外泄面板地址给外部站点
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

// assetETag 计算资源内容指纹（sha256 前 8 字节 hex），用作 ETag。
func assetETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// 四个嵌入资源的内容指纹，进程启动时算一次（嵌入内容在运行期不变）。
var (
	indexETag    = assetETag(indexHTML)
	appJSTag     = assetETag(appJS)
	mobileCSSTag = assetETag(mobileCSS)
	mobileJSTag  = assetETag(mobileJS)
)

// writeAsset 输出嵌入资源：安全头 + 内容指纹 ETag + Cache-Control: no-cache。
//
// 曾经这里只是裸的 w.Write，没有 ETag / Last-Modified / Cache-Control——浏览器
// 按启发式规则自行缓存，换二进制后仍可能继续用旧的 app.js / index.html，表现成
// "明明更新了却看不出变化"。no-cache 的语义是"可缓存，但每次使用前必须回源校验"：
// 内容没变回 304（省掉重复传输），内容变了指纹就变，浏览器必然重新拉取。
func writeAsset(w http.ResponseWriter, r *http.Request, contentType, etag string, body []byte) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// etagMatch 判断 If-None-Match 是否命中当前 ETag。
// 该头可能是逗号分隔的列表，也可能带 W/ 弱校验前缀，两者都要认。
func etagMatch(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "W/"))
		if part == etag || part == "*" {
			return true
		}
	}
	return false
}

// index 输出面板页面（静态无秘密；数据接口 /panel/api/* 才走鉴权）。
func (p *Panel) index(w http.ResponseWriter, r *http.Request) {
	writeAsset(w, r, "text/html; charset=utf-8", indexETag, indexHTML)
}

// appScript 输出前端逻辑（同源脚本，供 CSP script-src 'self' 加载）。
func (p *Panel) appScript(w http.ResponseWriter, r *http.Request) {
	writeAsset(w, r, "text/javascript; charset=utf-8", appJSTag, appJS)
}

// mobileStyle 输出手机端适配样式（同源样式表，CSP style-src 'self' 加载）。
// 单独成文而非写进 index.html：上游频繁改 index.html，独立文件可避免同步上游时反复冲突。
func (p *Panel) mobileStyle(w http.ResponseWriter, r *http.Request) {
	writeAsset(w, r, "text/css; charset=utf-8", mobileCSSTag, mobileCSS)
}

// mobileScript 输出手机端联动逻辑（同源脚本，须在 app.js 之后加载）。
func (p *Panel) mobileScript(w http.ResponseWriter, r *http.Request) {
	writeAsset(w, r, "text/javascript; charset=utf-8", mobileJSTag, mobileJS)
}
