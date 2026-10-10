// keyring.go 多 API Key：一份匹配表 + 逐 Key 的模型白名单与出口绑定。
//
// 单密钥部署下所有客户端共用一把钥匙，任一客户端（尤其带后台请求的桌面客户端）
// 都能调用任意模型并消耗额度。多 Key 让每个客户端/用途各拿一把，各自绑定允许的
// 模型与出口区域——越权请求在网关本地就被挡掉，不送上上游、不消耗任何额度。
//
// 匹配口径与 VerifyBearer 保持一致（SHA-256 摘要 + 常量时间比较），逐条比较不
// 提前返回，避免用时序区分「密钥存在」与「密钥不存在」。

package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// KeySpec 一条 API Key 的配置（多 Key 场景）。
type KeySpec struct {
	Key     string   // 密钥原文（仅内存持有，落盘在 config.json）
	Name    string   // 备注名（日志、面板与报错用；可空）
	Models  []string // 允许的模型（支持 * ? 通配；空 = 不限制）
	Realm   string   // 出口绑定："cn" / "global"；空 = 跟随请求
	Enabled bool     // 是否启用（缺省 true）
	// ID 统计归集用的稳定标识：sha256(Key) 前 8 字节的十六进制（16 字符，不可逆）。
	// 逐 Key 用量台账按它归集——台账里只出现这个指纹与 Name，永远不落密钥原文。
	ID string
}

// KeyID 计算密钥的统计标识（sha256 前 8 字节 hex）。
//
// 取 8 字节（64 位）而不是更短：这个值会写进 data/key-usage.json，长度必须足够
// 让不同密钥不会碰撞；同时又足够短，便于在面板上作为「无备注名的密钥」展示。
func KeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// SingleSpec 为单密钥模式构造 KeySpec（供逐 Key 用量归集）。
//
// key 为空表示未启用鉴权（开发模式），没有可归集的身份，返回 ok=false。
// Name 留空，展示层对空名回落「默认密钥」。
func SingleSpec(key string) (KeySpec, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return KeySpec{}, false
	}
	return KeySpec{Key: key, ID: KeyID(key)}, true
}

// Keyring 多条 API Key 的匹配表。
type Keyring struct {
	specs []KeySpec
}

// NewKeyring 构建匹配表；没有任何启用中的有效密钥时返回 nil（调用方回落单密钥）。
func NewKeyring(specs []KeySpec) *Keyring {
	enabled := make([]KeySpec, 0, len(specs))
	for _, s := range specs {
		if strings.TrimSpace(s.Key) == "" || !s.Enabled {
			continue
		}
		s.Key = strings.TrimSpace(s.Key)
		s.Name = strings.TrimSpace(s.Name)
		s.Realm = strings.ToLower(strings.TrimSpace(s.Realm))
		s.ID = KeyID(s.Key)
		enabled = append(enabled, s)
	}
	if len(enabled) == 0 {
		return nil
	}
	return &Keyring{specs: enabled}
}

// Len 返回启用中的密钥条数。
func (k *Keyring) Len() int {
	if k == nil {
		return 0
	}
	return len(k.specs)
}

// Specs 返回启用中的密钥配置副本（面板据此列出已配置的密钥，不暴露 key 原文之外
// 的额外信息——调用方自行决定展示哪些字段）。
func (k *Keyring) Specs() []KeySpec {
	if k == nil {
		return nil
	}
	out := make([]KeySpec, len(k.specs))
	for i, s := range k.specs {
		// Models 是切片，浅拷贝会让调用方改到匹配表内部状态（面板/测试都能改到），
		// 因此逐条深拷贝——配置量级很小，这点开销换"内部状态只由 NewKeyring 决定"。
		cp := s
		if len(s.Models) > 0 {
			cp.Models = append([]string(nil), s.Models...)
		}
		out[i] = cp
	}
	return out
}

// Match 解析请求头里的 Bearer 密钥并返回命中的配置。
func (k *Keyring) Match(r *http.Request) (KeySpec, bool) {
	if k == nil || len(k.specs) == 0 {
		return KeySpec{}, false
	}
	authz := r.Header.Get("Authorization")
	tok := ""
	if strings.HasPrefix(authz, bearerPrefix) {
		tok = authz[len(bearerPrefix):]
	}
	var found KeySpec
	hit := false
	for _, s := range k.specs {
		// 全部比较都执行（不提前 break），保持耗时与命中位置无关。
		if subtleCompare(tok, s.Key) {
			found = s
			hit = true
		}
	}
	return found, hit
}

// subtleCompare 常量时间比较两个字符串（先摘要再比较，长度差异被吸收）。
func subtleCompare(a, b string) bool {
	return subtle.ConstantTimeCompare(digest(a), digest(b)) == 1
}

// MatchModel 报告模型名是否命中白名单；patterns 为空表示不限制。
//
// 支持 * 与 ? 通配（大小写不敏感），例如 "gpt-6-*"、"deepseek*"。
func MatchModel(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	if model == "" {
		return false
	}
	m := strings.ToLower(model)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if globMatch(p, m) {
			return true
		}
	}
	return false
}

// globMatch 单模式 glob 匹配（* 任意长度、? 单字符）。
func globMatch(pattern, s string) bool {
	// 迭代式回溯：线性空间，避免递归在长输入上爆栈。
	var (
		pi, si         int
		star, matchEnd = -1, 0
	)
	for si < len(s) {
		switch {
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]):
			pi++
			si++
		case pi < len(pattern) && pattern[pi] == '*':
			star = pi
			matchEnd = si
			pi++
		case star >= 0:
			pi = star + 1
			matchEnd++
			si = matchEnd
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
