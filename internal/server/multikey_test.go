package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/keyusage"
)

// newKeyring 构建测试用多 Key 表。
func newKeyring(t *testing.T, specs ...httpauth.KeySpec) *httpauth.Keyring {
	t.Helper()
	kr := httpauth.NewKeyring(specs)
	if kr == nil {
		t.Fatal("keyring is nil")
	}
	return kr
}

// chatReq 造一条带指定 Bearer 的 chat 请求。
func chatReq(model, bearer string) *http.Request {
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

// TestMultiKeyModelLimit 覆盖模型白名单：命中放行，越权本地 400 且不打上游。
func TestMultiKeyModelLimit(t *testing.T) {
	calls := 0
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls++
		return 200, sseOK, true
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
		Keyring: newKeyring(t, httpauth.KeySpec{
			Key: "sk-codex", Name: "codex", Models: []string{"glm-*"}, Enabled: true,
		}),
	})

	// 1) 白名单内：放行。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chatReq("glm-5.2", "sk-codex"))
	if rec.Code != 200 {
		t.Fatalf("allowed model code=%d body=%s", rec.Code, rec.Body)
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d, want 1", calls)
	}

	// 2) 白名单外：本地 400，且不打上游、不消耗额度。
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, chatReq("gpt-6-astra", "sk-codex"))
	if rec2.Code != 400 {
		t.Fatalf("blocked model code=%d body=%s", rec2.Code, rec2.Body)
	}
	if calls != 1 {
		t.Fatalf("upstream called for blocked model (calls=%d)", calls)
	}
	var errResp map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("error body not json: %v", err)
	}
	if !strings.Contains(rec2.Body.String(), "model_not_allowed") {
		t.Errorf("error code missing: %s", rec2.Body)
	}
}

// TestMultiKeyAuthIsolation 未登记的密钥一律 401（多 Key 模式下不再回落顶层 api_key）。
func TestMultiKeyAuthIsolation(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
		APIKey:   "sk-legacy",
		Keyring: newKeyring(t, httpauth.KeySpec{
			Key: "sk-codex", Name: "codex", Enabled: true,
		}),
	})
	for _, tc := range []struct {
		header string
		want   int
	}{
		{"", 401},
		{"Bearer sk-legacy", 401}, // 多 Key 模式下顶层 api_key 不再是有效凭证
		{"Bearer sk-nope", 401},
		{"Bearer sk-codex", 200},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chatReq("glm-5.2", strings.TrimPrefix(tc.header, "Bearer ")))
		if rec.Code != tc.want {
			t.Errorf("header=%q code=%d, want %d (body=%s)", tc.header, rec.Code, tc.want, rec.Body)
		}
	}
}

// TestMultiKeyRealmBinding 出口绑定：Key 指定 global 时只选 global 账号。
func TestMultiKeyRealmBinding(t *testing.T) {
	var usedAuth string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		usedAuth = authz
		return 200, sseOK, true
	})
	globalAcct := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999}
	if _, err := auth.BackfillRealmFor(globalAcct, "global"); err != nil {
		t.Fatalf("backfill realm: %v", err)
	}
	p := testPoolWith(
		&auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999},
		globalAcct,
	)
	h := NewHandler(Config{
		Pool:     p,
		Upstream: up,
		Keyring: newKeyring(t, httpauth.KeySpec{
			Key: "sk-intl", Name: "intl", Realm: "global", Enabled: true,
		}),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chatReq("glm-5.2", "sk-intl"))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if usedAuth != "Bearer at-global" {
		t.Errorf("upstream auth=%q, want global account", usedAuth)
	}
}

// TestKeyUsageLedgerPerKey 逐 Key 用量按指纹归集：不同密钥各自成行，
// 且被白名单拦下的请求也计入该密钥的失败数（面板靠它看出"某把钥匙在尝试越权"）。
func TestKeyUsageLedgerPerKey(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	ku := keyusage.New("")
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
		Keyring: newKeyring(t,
			httpauth.KeySpec{Key: "sk-a", Name: "A", Enabled: true},
			httpauth.KeySpec{Key: "sk-b", Name: "B", Enabled: true, Models: []string{"glm-*"}},
		),
		KeyUsage: ku,
	})

	// sk-a：两次合法请求。
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chatReq("glm-5.2", "sk-a"))
		if rec.Code != 200 {
			t.Fatalf("sk-a code=%d body=%s", rec.Code, rec.Body)
		}
	}
	// sk-b：一次合法 + 一次越权（后者计失败）。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chatReq("glm-5.2", "sk-b"))
	if rec.Code != 200 {
		t.Fatalf("sk-b allowed code=%d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, chatReq("gpt-6-astra", "sk-b"))
	if rec2.Code != 400 {
		t.Fatalf("sk-b blocked code=%d, want 400", rec2.Code)
	}

	aggs := ku.Snapshot(7)
	if len(aggs) != 2 {
		t.Fatalf("台账行数 = %d, want 2 (%+v)", len(aggs), aggs)
	}
	byID := map[string]keyusage.KeyAgg{}
	for _, a := range aggs {
		byID[a.KeyID] = a
	}
	a := byID[httpauth.KeyID("sk-a")]
	if a.Requests != 2 || a.Errors != 0 {
		t.Errorf("sk-a req/err = %d/%d, want 2/0", a.Requests, a.Errors)
	}
	if a.Label != "A" {
		t.Errorf("sk-a label = %q, want A", a.Label)
	}
	b := byID[httpauth.KeyID("sk-b")]
	if b.Requests != 2 || b.Errors != 1 {
		t.Errorf("sk-b req/err = %d/%d, want 2/1", b.Requests, b.Errors)
	}
	// 台账不得以任何形式记录密钥原文。
	for _, agg := range aggs {
		if strings.Contains(agg.KeyID, "sk-") {
			t.Errorf("台账出现疑似密钥原文: %q", agg.KeyID)
		}
	}
}

// TestSingleKeyModeStillCountsUsage 单密钥模式下同样归集用量（那也是一把钥匙），
// 且未配置 api_key（不鉴权）时不产生任何行。
func TestSingleKeyModeStillCountsUsage(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	pool := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})

	ku := keyusage.New("")
	h := NewHandler(Config{Pool: pool, Upstream: up, APIKey: "sk-solo", KeyUsage: ku})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chatReq("glm-5.2", "sk-solo"))
	if rec.Code != 200 {
		t.Fatalf("单密钥放行失败: %d %s", rec.Code, rec.Body)
	}
	aggs := ku.Snapshot(7)
	if len(aggs) != 1 || aggs[0].KeyID != httpauth.KeyID("sk-solo") || aggs[0].Requests != 1 {
		t.Fatalf("单密钥台账 = %+v", aggs)
	}

	// 不鉴权（api_key 为空）：没有可归集身份，不产生新行。
	ku2 := keyusage.New("")
	h2 := NewHandler(Config{Pool: pool, Upstream: up, KeyUsage: ku2})
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, chatReq("glm-5.2", ""))
	if aggs := ku2.Snapshot(7); len(aggs) != 0 {
		t.Fatalf("未鉴权模式不应产生台账行: %+v", aggs)
	}
}
