// apikeys.go 面板「用量 → API Key 用量」接口：逐把密钥的用量 + 配置对照。
//
// 与 /panel/api/usage 的分工：那个按账号/模型回答"额度去哪了"；这个按**密钥**回答
// "哪把钥匙用了多少"。两者数据源不同（usage.json 按账号分桶，key-usage.json 按
// 密钥指纹分桶），不能互相推导，因此各自读各自的台账。
//
// 返回的行集合是「配置里启用的密钥」与「台账里有记录的密钥」的并集：
//   - 配置里配了但一次没用过 → 请求数为 0，configured=true（"配了没用"一眼可见）；
//   - 台账里有但配置里已删掉 → configured=false（历史用量不因删配置而消失）。
package panel

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/keyusage"
)

// keyUsageRow 一把密钥在查询窗口内的用量 + 它在配置里的现状。
type keyUsageRow struct {
	KeyID string `json:"key_id"`          // 密钥指纹（sha256 前 8 字节 hex）
	Label string `json:"label,omitempty"` // 备注名：配置里的优先，其次台账里记的
	// Models / Realm 是该密钥的授权范围（配置侧），用量行据此可以回答
	// "这把钥匙被允许调什么、实际调了什么"。
	Models           []string  `json:"models,omitempty"`
	Realm            string    `json:"realm,omitempty"`
	Configured       bool      `json:"configured"`          // 是否仍在当前配置里
	Enabled          bool      `json:"enabled"`             // 配置里是否启用（未配置时为 false）
	Requests         int64     `json:"requests"`            // 请求数
	Errors           int64     `json:"errors"`              // 失败数
	PromptTokens     int64     `json:"prompt_tokens"`       // prompt tokens
	CompletionTokens int64     `json:"completion_tokens"`   // completion tokens
	TotalTokens      int64     `json:"total_tokens"`        // total tokens
	Credit           float64   `json:"credit"`              // 积分扣除累计
	CreditSamples    int64     `json:"credit_samples"`      // 积分观测样本数
	LastUsed         time.Time `json:"last_used,omitempty"` // 最后一次使用
	ActiveDays       int       `json:"active_days"`         // 窗口内有记录的天数
}

// keyUsage 返回逐 API Key 用量。
//
// days：查询窗口（1..400）；缺省 7，days=0 表示全部保留窗口。
// 台账未装配（未启用 keyusage 记录器）时返回 501。
func (p *Panel) keyUsage(w http.ResponseWriter, r *http.Request) {
	if p.cfg.KeyUsage == nil {
		writeErr(w, http.StatusNotImplemented, "key usage not available")
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}
	if days > 400 {
		days = 400
	}

	// 配置侧：多 Key 列出全部已配置密钥（含零用量），单 Key 列默认密钥。
	specs, multi := p.configuredKeySpecs()

	aggs := p.cfg.KeyUsage.Snapshot(days)
	byID := make(map[string]keyusage.KeyAgg, len(aggs))
	for _, a := range aggs {
		byID[a.KeyID] = a
	}

	rows := make([]keyUsageRow, 0, len(specs)+len(aggs))
	seen := make(map[string]bool, len(specs))
	for _, s := range specs {
		if seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		rows = append(rows, rowFor(s.ID, s.Name, true, true, s.Models, s.Realm, byID[s.ID]))
	}
	// 配置里已删除、但台账仍有记录的密钥：按指纹列出（label 取台账里最后一次的备注名）。
	for _, a := range aggs {
		if seen[a.KeyID] {
			continue
		}
		seen[a.KeyID] = true
		rows = append(rows, rowFor(a.KeyID, a.Label, false, false, nil, "", a))
	}

	// 排序：先按请求数降序，同数量按最近使用时间降序（让"正在用的"浮到顶部）。
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		return rows[i].LastUsed.After(rows[j].LastUsed)
	})

	var totals keyUsageTotals
	for _, row := range rows {
		totals.Requests += row.Requests
		totals.Errors += row.Errors
		totals.PromptTokens += row.PromptTokens
		totals.CompletionTokens += row.CompletionTokens
		totals.TotalTokens += row.TotalTokens
		totals.Credit += row.Credit
		if !row.Configured {
			totals.Removed++
		}
	}
	totals.Keys = len(rows)

	writeJSON(w, http.StatusOK, map[string]any{
		"entries":    rows,
		"days":       days,
		"multi":      multi,
		"configured": len(specs),
		"totals":     totals,
	})
}

// keyUsageTotals 合计行（前端 KPI 卡直接用，避免前端再算一遍口径不一致）。
type keyUsageTotals struct {
	Requests         int64   `json:"requests"`
	Errors           int64   `json:"errors"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	Credit           float64 `json:"credit"`
	Keys             int     `json:"keys"`
	Removed          int     `json:"removed"` // 配置里已删除但仍有用量记录的密钥数
}

// rowFor 组装一行（配置侧身份 + 台账聚合）。
func rowFor(keyID, label string, configured, enabled bool, models []string, realm string, agg keyusage.KeyAgg) keyUsageRow {
	row := keyUsageRow{
		KeyID:      keyID,
		Label:      label,
		Models:     models,
		Realm:      realm,
		Configured: configured,
		Enabled:    enabled,
	}
	// 配置里没写备注名时，用台账里记过的名字兜底（改名前的历史行仍然可读）。
	if row.Label == "" {
		row.Label = agg.Label
	}
	row.Requests = agg.Requests
	row.Errors = agg.Errors
	row.PromptTokens = agg.PromptTokens
	row.CompletionTokens = agg.CompletionTokens
	row.TotalTokens = agg.TotalTokens
	row.Credit = agg.Credit
	row.CreditSamples = agg.CreditSamples
	row.LastUsed = agg.LastUsed
	row.ActiveDays = agg.ActiveDays
	return row
}

// configuredKeySpecs 返回当前配置里的密钥身份（热改优先）。
//
// 多 Key：全部启用中的 api_keys；单 Key：顶层 api_key 合成的一条（Name 留空，
// 展示层回落"默认密钥"）；未配置任何密钥（不鉴权）：空表。
func (p *Panel) configuredKeySpecs() ([]httpauth.KeySpec, bool) {
	keyring := p.cfg.Keyring
	if p.cfg.Live != nil {
		keyring = p.cfg.Live.Load().Keyring
	}
	if keyring != nil {
		if specs := keyring.Specs(); len(specs) > 0 {
			return specs, true
		}
	}
	// 单密钥模式：这里与 withAuth 走同一个 apiKey()（Live 优先），
	// 否则改了密钥后面板列表会指向一把已经不再生效的旧钥匙。
	if spec, ok := httpauth.SingleSpec(p.apiKey()); ok {
		return []httpauth.KeySpec{spec}, false
	}
	return nil, false
}
