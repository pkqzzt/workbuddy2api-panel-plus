// Package keyusage 逐 API Key 的用量台账，供面板「用量 → API Key 用量」展示。
//
// 与 internal/usage 的分工：usage 按 (时间片, realm, uid, model, rate) 分桶，回答
// 「哪个账号/模型用了多少」；本包只按 (日期, API Key) 分桶，回答「哪把钥匙用了多少」
// ——这是多 Key 部署下的配额观测视角，与账号维度正交，因此另立台账而不是把 key
// 塞进 usage 的桶键（那会让 usage.json 的格式版本再翻一代，且旧数据无法回填）。
//
// 落盘：data/key-usage.json，原子替换 + 防抖刷新（默认 30s），重启不丢。
// 台账里**只有密钥指纹与备注名**（KeyID = sha256(key) 前 8 字节 hex），永远不落
// 密钥原文——面板读这个文件也不会泄露凭证。
//
// 保留策略：按日分桶，只保留最近 retainDays 天的桶（默认 400 天）。桶数上界
// ≈ 密钥数 × 保留天数，实测单桶约 120 字节。
package keyusage

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// flushInterval 防抖落盘间隔。
const flushInterval = 30 * time.Second

// defaultRetainDays 桶保留天数（按本地时区日期计）。
const defaultRetainDays = 400

// dayLayout 分片键的日期格式（本地时区，与用户直觉一致）。
const dayLayout = "2006-01-02"

// fileVersion 是 key-usage.json 的当前格式版本。
const fileVersion = 1

// bucket 一个 (日期, API Key) 的累计量。JSON 字段名刻意取短，因为桶数随时间增长。
type bucket struct {
	Day   string  `json:"d"`              // 本地时区日期 "2006-01-02"
	KeyID string  `json:"k"`              // 密钥指纹（sha256 前 8 字节 hex）
	Label string  `json:"l,omitempty"`    // 备注名快照（配置改名后以最新一次为准）
	Req   int64   `json:"q"`              // 请求数
	Err   int64   `json:"e"`              // 失败数
	PT    int64   `json:"p"`              // prompt tokens
	CT    int64   `json:"c"`              // completion tokens
	TT    int64   `json:"t"`              // total tokens
	CR    float64 `json:"cr,omitempty"`   // 积分扣除累计（仅明确存在的观测）
	CRN   int64   `json:"cn,omitempty"`   // 积分观测样本数（区分缺字段与真实 0）
	Last  int64   `json:"last,omitempty"` // 最后一次使用（unix 秒）
}

// file 落盘结构。
type file struct {
	Version int      `json:"version"`
	Saved   string   `json:"saved"`
	Buckets []bucket `json:"buckets"`
}

// Recorder 并发安全的逐 Key 用量记录器。
type Recorder struct {
	mu         sync.Mutex
	path       string
	retainDays int
	buckets    map[string]*bucket // key: day|keyID
	dirty      bool

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// New 创建记录器。path 为空时禁用落盘（纯内存，测试用）。
func New(path string) *Recorder {
	r := &Recorder{
		path:       path,
		retainDays: defaultRetainDays,
		buckets:    make(map[string]*bucket),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	if path != "" {
		if err := r.load(); err != nil {
			log.Printf("[keyusage] 读取 %s 失败（从零开始）: %v", path, err)
		}
	}
	return r
}

// Start 启动后台防抖落盘。Stop 前一直运行。
func (r *Recorder) Start() {
	go func() {
		defer close(r.done)
		t := time.NewTicker(flushInterval)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				r.flush(true)
				return
			case <-t.C:
				r.flush(false)
			}
		}
	}()
}

// Stop 停止后台循环并做最后一次落盘。
func (r *Recorder) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
}

// Save 立即落盘（进程退出前的显式保存）。
func (r *Recorder) Save() {
	if r == nil {
		return
	}
	r.flush(true)
}

// Note 记录一次请求。keyID 为空表示该请求没有可归集的密钥身份（未启用鉴权），
// 直接丢弃而不是记到某个"未知"桶里——那只会让面板出现一行永远不能对应到配置的噪音。
//
// token/credit 取**请求最终值**（不是逐次上游尝试的累加）：失败重试的中间尝试通常
// 没有 usage 观测，而成功的那个尝试就是这次请求的实际消耗，按请求记一次才不会重复。
func (r *Recorder) Note(keyID, label string, ok bool, pt, ct, tt int64, credit float64, hasCredit bool) {
	if r == nil || keyID == "" {
		return
	}
	now := time.Now()
	day := now.Format(dayLayout)
	mapKey := day + "|" + keyID

	r.mu.Lock()
	defer r.mu.Unlock()

	b := r.buckets[mapKey]
	if b == nil {
		b = &bucket{Day: day, KeyID: keyID}
		r.buckets[mapKey] = b
	}
	if label != "" {
		b.Label = label
	}
	b.Req++
	if !ok {
		b.Err++
	}
	if pt > 0 {
		b.PT += pt
	}
	if ct > 0 {
		b.CT += ct
	}
	if tt > 0 {
		b.TT += tt
	} else if pt > 0 || ct > 0 {
		b.TT += pt + ct
	}
	if hasCredit {
		b.CR += credit
		b.CRN++
	}
	b.Last = now.Unix()
	r.dirty = true
}

// KeyAgg 一个 API Key 在查询窗口内的聚合。
type KeyAgg struct {
	KeyID            string    `json:"key_id"`              // 密钥指纹
	Label            string    `json:"label,omitempty"`     // 备注名（最近一次记录的）
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

// Snapshot 返回逐 Key 聚合（按请求数降序）。days<=0 表示全部保留窗口。
func (r *Recorder) Snapshot(days int) []KeyAgg {
	if r == nil {
		return nil
	}
	// 窗口起点：本地时区今天 0 点往前推 days-1 天；字符串比较即可定序（ISO 日期）。
	minDay := ""
	if days > 0 {
		minDay = time.Now().AddDate(0, 0, -(days - 1)).Format(dayLayout)
	}
	acc := map[string]*KeyAgg{}
	labelDay := map[string]string{}

	r.mu.Lock()
	for _, b := range r.buckets {
		if minDay != "" && b.Day < minDay {
			continue
		}
		a := acc[b.KeyID]
		if a == nil {
			a = &KeyAgg{KeyID: b.KeyID}
			acc[b.KeyID] = a
		}
		a.Requests += b.Req
		a.Errors += b.Err
		a.PromptTokens += b.PT
		a.CompletionTokens += b.CT
		a.TotalTokens += b.TT
		a.Credit += b.CR
		a.CreditSamples += b.CRN
		a.ActiveDays++
		if b.Last > 0 {
			if t := time.Unix(b.Last, 0); t.After(a.LastUsed) {
				a.LastUsed = t
			}
		}
		// 备注名以最新一天为准：配置改名后，面板显示的是当前名字。
		if b.Label != "" && b.Day >= labelDay[b.KeyID] {
			labelDay[b.KeyID] = b.Day
			a.Label = b.Label
		}
	}
	r.mu.Unlock()

	out := make([]KeyAgg, 0, len(acc))
	for _, a := range acc {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].KeyID < out[j].KeyID
	})
	return out
}

// pruneLocked 丢弃超出保留窗口的桶。调用方须持有 r.mu。
func (r *Recorder) pruneLocked(now time.Time) {
	minDay := now.AddDate(0, 0, -r.retainDays).Format(dayLayout)
	removed := 0
	for k, b := range r.buckets {
		if b.Day < minDay {
			delete(r.buckets, k)
			removed++
		}
	}
	if removed > 0 {
		r.dirty = true
		log.Printf("[keyusage] 裁剪 %d 个过期桶（保留 %d 天）", removed, r.retainDays)
	}
}

func (r *Recorder) flush(force bool) {
	if r == nil || r.path == "" {
		return
	}
	r.mu.Lock()
	if !r.dirty && !force {
		r.mu.Unlock()
		return
	}
	r.pruneLocked(time.Now())
	buckets := make([]bucket, 0, len(r.buckets))
	for _, b := range r.buckets {
		buckets = append(buckets, *b)
	}
	r.dirty = false
	r.mu.Unlock()

	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Day != buckets[j].Day {
			return buckets[i].Day < buckets[j].Day
		}
		return buckets[i].KeyID < buckets[j].KeyID
	})
	data, err := json.MarshalIndent(file{
		Version: fileVersion,
		Saved:   time.Now().Format(time.RFC3339),
		Buckets: buckets,
	}, "", "  ")
	if err != nil {
		log.Printf("[keyusage] 序列化失败: %v", err)
		return
	}
	tmp := r.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		log.Printf("[keyusage] 建目录失败: %v", err)
		return
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[keyusage] 写 %s 失败: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		log.Printf("[keyusage] 替换 %s 失败: %v", r.path, err)
	}
}

func (r *Recorder) load() error {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	for _, b := range f.Buckets {
		if b.Day == "" || b.KeyID == "" {
			continue
		}
		cp := b
		r.buckets[b.Day+"|"+b.KeyID] = &cp
	}
	return nil
}

// Describe 返回一行台账状态（启动日志用）。
func (r *Recorder) Describe() string {
	if r == nil {
		return "keyusage: disabled"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := map[string]bool{}
	for _, b := range r.buckets {
		keys[b.KeyID] = true
	}
	return "keyusage: " + strconv.Itoa(len(keys)) + " keys / " + strconv.Itoa(len(r.buckets)) + " day-buckets"
}
