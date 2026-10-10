package httpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestNewKeyringSkipsDisabledAndEmpty(t *testing.T) {
	if kr := NewKeyring(nil); kr != nil {
		t.Fatalf("empty specs should yield nil keyring, got %v", kr)
	}
	kr := NewKeyring([]KeySpec{
		{Key: "", Enabled: true},
		{Key: "sk-a", Enabled: false},
	})
	if kr != nil {
		t.Fatalf("all-disabled should yield nil keyring, got %v", kr)
	}
	kr = NewKeyring([]KeySpec{{Key: "sk-a", Enabled: true}, {Key: "sk-b", Enabled: false}})
	if kr == nil || kr.Len() != 1 {
		t.Fatalf("keyring = %v (len %d), want 1", kr, kr.Len())
	}
}

func TestKeyringMatch(t *testing.T) {
	kr := NewKeyring([]KeySpec{
		{Key: "sk-codex", Name: "codex", Enabled: true},
		{Key: "sk-phone", Name: "phone", Enabled: true},
	})
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer sk-codex", "codex", true},
		{"Bearer sk-phone", "phone", true},
		{"Bearer sk-nope", "", false},
		{"", "", false},
		{"sk-codex", "", false}, // 缺 Bearer 前缀
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		spec, ok := kr.Match(req)
		if ok != c.ok || spec.Name != c.want {
			t.Errorf("Match(%q) = (%q,%v), want (%q,%v)", c.header, spec.Name, ok, c.want, c.ok)
		}
	}
}

func TestMatchModel(t *testing.T) {
	cases := []struct {
		patterns []string
		model    string
		want     bool
	}{
		{nil, "anything", true},                                  // 空 = 不限制
		{[]string{}, "anything", true},                           // 同上
		{[]string{"glm-5.2"}, "glm-5.2", true},                   // 精确
		{[]string{"glm-5.2"}, "glm-5.3", false},                  // 不命中
		{[]string{"deepseek*"}, "deepseek-v4.1-flash", true},     // 前缀通配
		{[]string{"*"}, "anything", true},                        // 全放行
		{[]string{"gpt-6-astr?"}, "gpt-6-astra", true},           // 单字符通配
		{[]string{"GPT-6-ASTRA"}, "gpt-6-astra", true},           // 大小写不敏感
		{[]string{" glm-5.2 "}, "glm-5.2", true},                 // 去空白
		{[]string{"glm-5.2", "kimi-k3"}, "kimi-k3", true},        // 多模式任一命中
		{[]string{"glm-5.2", "kimi-k3"}, "deepseek-v4.1", false}, // 多模式全不中
		{[]string{"glm-5.2"}, "", false},                         // 空模型名
	}
	for _, c := range cases {
		if got := MatchModel(c.patterns, c.model); got != c.want {
			t.Errorf("MatchModel(%v, %q) = %v, want %v", c.patterns, c.model, got, c.want)
		}
	}
}

func TestGlobMatchEdgeCases(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"*b*", "ab", true},
		{"a*", "a", true},
		{"*", "", true},
		{"?", "", false},
		{"a?c", "abc", true},
		{"", "", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

// TestKeyIDStableAndDistinct 台账按指纹归集：同一密钥必须恒定，不同密钥必须不同，
// 且指纹不能等于（或包含）密钥原文——它会被写进 key-usage.json 并透出给面板。
func TestKeyIDStableAndDistinct(t *testing.T) {
	a1, a2 := KeyID("sk-alpha"), KeyID("sk-alpha")
	b := KeyID("sk-beta")
	if a1 != a2 {
		t.Errorf("KeyID 不稳定: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("不同密钥得到同一指纹: %q", a1)
	}
	if len(a1) != 16 {
		t.Errorf("指纹长度 = %d, want 16（sha256 前 8 字节 hex）", len(a1))
	}
	if a1 == "sk-alpha" || len(a1) == 0 {
		t.Errorf("指纹不得等于密钥原文: %q", a1)
	}
}

// TestKeyringFillsID NewKeyring 必须把指纹填进 spec，否则用量台账拿不到归集键。
func TestKeyringFillsID(t *testing.T) {
	kr := NewKeyring([]KeySpec{{Key: " sk-pad ", Name: " pad ", Realm: " CN ", Enabled: true}})
	specs := kr.Specs()
	if len(specs) != 1 {
		t.Fatalf("specs len = %d, want 1", len(specs))
	}
	s := specs[0]
	if s.Key != "sk-pad" {
		t.Errorf("Key 未去空白: %q", s.Key)
	}
	if s.Name != "pad" {
		t.Errorf("Name 未去空白: %q", s.Name)
	}
	if s.Realm != "cn" {
		t.Errorf("Realm 未归一化: %q", s.Realm)
	}
	if s.ID != KeyID("sk-pad") {
		t.Errorf("ID = %q, want %q", s.ID, KeyID("sk-pad"))
	}
}

// TestKeyringSpecsIsCopy Specs 返回副本：面板回调拿到后改动不能污染匹配表。
func TestKeyringSpecsIsCopy(t *testing.T) {
	kr := NewKeyring([]KeySpec{{Key: "sk-a", Models: []string{"*"}, Enabled: true}})
	specs := kr.Specs()
	specs[0].Key = "sk-tampered"
	specs[0].Models[0] = "nothing"
	if got := kr.Specs()[0]; got.Key != "sk-a" || got.Models[0] != "*" {
		t.Errorf("Specs 未隔离：内部状态被外部改动污染 (%+v)", got)
	}
}

// TestSingleSpec 单密钥模式的身份：空 key（不鉴权）没有可归集身份，须返回 false——
// 否则台账会出现一行永远对不上任何配置的"未知密钥"。
func TestSingleSpec(t *testing.T) {
	if _, ok := SingleSpec(""); ok {
		t.Error("空 api_key（不鉴权）不应产生身份")
	}
	if _, ok := SingleSpec("   "); ok {
		t.Error("纯空白 api_key 不应产生身份")
	}
	spec, ok := SingleSpec(" sk-solo ")
	if !ok {
		t.Fatal("非空 api_key 应产生身份")
	}
	if spec.ID != KeyID("sk-solo") {
		t.Errorf("ID = %q, want %q", spec.ID, KeyID("sk-solo"))
	}
	if spec.Name != "" {
		t.Errorf("单密钥未命名语义：Name 应为空，得到 %q", spec.Name)
	}
}

// TestKeyringNilSafety nil keyring（未启用多 Key）上的方法必须安全可调用。
func TestKeyringNilSafety(t *testing.T) {
	var kr *Keyring
	if kr.Len() != 0 {
		t.Error("nil keyring Len 应为 0")
	}
	if kr.Specs() != nil {
		t.Error("nil keyring Specs 应为 nil")
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if _, ok := kr.Match(req); ok {
		t.Error("nil keyring 不应匹配任何请求")
	}
}
