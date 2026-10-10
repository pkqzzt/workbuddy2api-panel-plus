package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPanelAssetsCacheHeaders 面板静态资源必须带 ETag 与 no-cache。
//
// 裸写响应体会让浏览器按启发式规则自行缓存，换二进制后仍可能继续用旧界面
// （表现成"更新了却看不出变化"）。这里逐资源断言三个往返。
func TestPanelAssetsCacheHeaders(t *testing.T) {
	p := New(Config{})
	for _, path := range []string{"/panel/", "/panel/app.js", "/panel/mobile.css", "/panel/mobile.js"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("首次请求 code=%d, want 200", rec.Code)
			}
			etag := rec.Header().Get("ETag")
			if etag == "" {
				t.Fatal("缺少 ETag")
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("Cache-Control=%q, want no-cache", cc)
			}
			if rec.Body.Len() == 0 {
				t.Error("响应体为空")
			}

			// 内容未变：带 If-None-Match 复请求应 304 且无正文（省掉重复传输）。
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("If-None-Match", etag)
			rec2 := httptest.NewRecorder()
			p.ServeHTTP(rec2, req)
			if rec2.Code != http.StatusNotModified {
				t.Errorf("条件请求 code=%d, want 304", rec2.Code)
			}
			if rec2.Body.Len() != 0 {
				t.Errorf("304 不应带正文，实际 %d 字节", rec2.Body.Len())
			}

			// 内容已变（旧 ETag）：必须回 200 全量，保证更新后能拿到新资源。
			req2 := httptest.NewRequest(http.MethodGet, path, nil)
			req2.Header.Set("If-None-Match", `"stale-tag"`)
			rec3 := httptest.NewRecorder()
			p.ServeHTTP(rec3, req2)
			if rec3.Code != http.StatusOK || rec3.Body.Len() == 0 {
				t.Errorf("旧 ETag 应回 200 + 正文，得到 code=%d len=%d", rec3.Code, rec3.Body.Len())
			}
		})
	}
}

// TestAssetETagDistinct 不同资源的内容指纹必须互不相同，否则浏览器会拿旧缓存当新版。
func TestAssetETagDistinct(t *testing.T) {
	tags := map[string]string{
		"index": indexETag, "app.js": appJSTag, "mobile.css": mobileCSSTag, "mobile.js": mobileJSTag,
	}
	seen := map[string]string{}
	for name, tag := range tags {
		if !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
			t.Errorf("%s 的 ETag 必须带引号（HTTP 规范），得到 %q", name, tag)
		}
		if prev, dup := seen[tag]; dup {
			t.Errorf("%s 与 %s 的 ETag 相同（%s）", name, prev, tag)
		}
		seen[tag] = name
	}
}

func TestEtagMatch(t *testing.T) {
	cases := []struct {
		header, etag string
		want         bool
	}{
		{`"abc"`, `"abc"`, true},
		{`W/"abc"`, `"abc"`, true},
		{`"x", "abc"`, `"abc"`, true},
		{`*`, `"abc"`, true},
		{`"nope"`, `"abc"`, false},
		{"", `"abc"`, false},
	}
	for _, c := range cases {
		if got := etagMatch(c.header, c.etag); got != c.want {
			t.Errorf("etagMatch(%q, %q) = %v, want %v", c.header, c.etag, got, c.want)
		}
	}
}
