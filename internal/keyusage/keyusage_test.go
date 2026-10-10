package keyusage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNoteAggregatesByKey(t *testing.T) {
	r := New("")
	r.Note("id-a", "Codex", true, 100, 10, 110, 1.5, true)
	r.Note("id-a", "Codex", true, 200, 20, 220, 2.5, true)
	r.Note("id-b", "手机端", false, 0, 0, 0, 0, false)

	aggs := r.Snapshot(7)
	if len(aggs) != 2 {
		t.Fatalf("keys = %d, want 2 (%+v)", len(aggs), aggs)
	}
	// 排序：请求数降序 → id-a（2 次）在前。
	if aggs[0].KeyID != "id-a" {
		t.Fatalf("首行 = %q, want id-a", aggs[0].KeyID)
	}
	a := aggs[0]
	if a.Requests != 2 || a.Errors != 0 {
		t.Errorf("id-a req/err = %d/%d, want 2/0", a.Requests, a.Errors)
	}
	if a.PromptTokens != 300 || a.CompletionTokens != 30 || a.TotalTokens != 330 {
		t.Errorf("id-a tokens = %d/%d/%d, want 300/30/330", a.PromptTokens, a.CompletionTokens, a.TotalTokens)
	}
	if a.Credit != 4.0 || a.CreditSamples != 2 {
		t.Errorf("id-a credit = %v (%d samples), want 4 (2)", a.Credit, a.CreditSamples)
	}
	if a.Label != "Codex" {
		t.Errorf("id-a label = %q, want Codex", a.Label)
	}
	if a.LastUsed.IsZero() {
		t.Error("id-a LastUsed 不应为零值")
	}
	b := aggs[1]
	if b.Requests != 1 || b.Errors != 1 {
		t.Errorf("id-b req/err = %d/%d, want 1/1", b.Requests, b.Errors)
	}
	if b.CreditSamples != 0 {
		t.Errorf("未观测到 credit 的请求不应计入样本数，得到 %d", b.CreditSamples)
	}
}

// TestNoteWithoutTotalFallsBackToPromptPlusCompletion 上游只给 pt/ct 时用 pt+ct 兜底，
// 保证总量口径连续（与 internal/usage 同规则）。
func TestNoteWithoutTotalFallsBackToPromptPlusCompletion(t *testing.T) {
	r := New("")
	r.Note("id-a", "", true, 7, 3, 0, 0, false)
	aggs := r.Snapshot(1)
	if len(aggs) != 1 || aggs[0].TotalTokens != 10 {
		t.Fatalf("total = %+v, want 10", aggs)
	}
}

// TestNoteSkipsEmptyKeyID 没有身份（未启用鉴权）的请求不进台账：
// 否则面板会出现一行对不上任何配置的"未知密钥"。
func TestNoteSkipsEmptyKeyID(t *testing.T) {
	r := New("")
	r.Note("", "默认", true, 100, 10, 110, 1, true)
	if aggs := r.Snapshot(7); len(aggs) != 0 {
		t.Fatalf("空 keyID 不应产生记录: %+v", aggs)
	}
}

// TestSnapshotWindowFilter days 窗口按本地日期过滤：超窗桶不计入，全部窗口(days=0)计入。
func TestSnapshotWindowFilter(t *testing.T) {
	r := New("")
	now := time.Now()
	// 手工塞两个桶：今天与 10 天前。
	r.buckets[now.Format(dayLayout)+"|old"] = &bucket{Day: now.Format(dayLayout), KeyID: "old", Req: 5}
	r.buckets[now.AddDate(0, 0, -10).Format(dayLayout)+"|old"] = &bucket{
		Day: now.AddDate(0, 0, -10).Format(dayLayout), KeyID: "old", Req: 3,
	}

	if aggs := r.Snapshot(7); len(aggs) != 1 || aggs[0].Requests != 5 {
		t.Fatalf("近 7 天应只含今天的桶: %+v", aggs)
	}
	if aggs := r.Snapshot(30); len(aggs) != 1 || aggs[0].Requests != 8 {
		t.Fatalf("近 30 天应含两个桶: %+v", aggs)
	}
	if aggs := r.Snapshot(0); len(aggs) != 1 || aggs[0].Requests != 8 {
		t.Fatalf("全部窗口应含两个桶: %+v", aggs)
	}
}

// TestLabelFollowsLatestDay 备注名以最新一天为准：改配置名之后，旧桶里的旧名
// 不该把面板显示拉回历史。
func TestLabelFollowsLatestDay(t *testing.T) {
	r := New("")
	now := time.Now()
	old := now.AddDate(0, 0, -3)
	r.buckets[old.Format(dayLayout)+"|k"] = &bucket{Day: old.Format(dayLayout), KeyID: "k", Req: 1, Label: "旧名"}
	r.buckets[now.Format(dayLayout)+"|k"] = &bucket{Day: now.Format(dayLayout), KeyID: "k", Req: 1, Label: "新名"}
	aggs := r.Snapshot(7)
	if len(aggs) != 1 || aggs[0].Label != "新名" {
		t.Fatalf("label = %+v, want 新名", aggs)
	}
}

func TestPruneDropsOldBuckets(t *testing.T) {
	r := New("")
	r.retainDays = 5
	now := time.Now()
	r.mu.Lock()
	r.buckets[now.Format(dayLayout)+"|keep"] = &bucket{Day: now.Format(dayLayout), KeyID: "keep", Req: 1}
	r.buckets[now.AddDate(0, 0, -6).Format(dayLayout)+"|drop"] = &bucket{
		Day: now.AddDate(0, 0, -6).Format(dayLayout), KeyID: "drop", Req: 1,
	}
	r.pruneLocked(now)
	r.mu.Unlock()

	if len(r.buckets) != 1 {
		t.Fatalf("裁剪后桶数 = %d, want 1", len(r.buckets))
	}
	if _, ok := r.buckets[now.Format(dayLayout)+"|keep"]; !ok {
		t.Error("窗口内的桶被误删")
	}
}

// TestPersistAndReload 落盘—重载闭环：重启后历史用量不能丢。
func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-usage.json")

	r := New(path)
	r.Note("id-a", "Codex", true, 10, 5, 15, 0.5, true)
	r.Save()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	// 文件里不得出现密钥原文（这里用指纹做冒烟断言：内容里应能解析出 key id）。
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if f.Version != fileVersion || len(f.Buckets) != 1 {
		t.Fatalf("落盘内容异常: %+v", f)
	}

	r2 := New(path)
	aggs := r2.Snapshot(7)
	if len(aggs) != 1 || aggs[0].Requests != 1 || aggs[0].TotalTokens != 15 {
		t.Fatalf("重载后聚合 = %+v", aggs)
	}
	if aggs[0].Label != "Codex" {
		t.Errorf("重载后 label = %q, want Codex", aggs[0].Label)
	}
}

// TestLoadCorruptFileStartsFromZero 损坏文件不能让进程起不来（与其它台账同规则）。
func TestLoadCorruptFileStartsFromZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	if aggs := r.Snapshot(7); len(aggs) != 0 {
		t.Fatalf("损坏文件应从零开始: %+v", aggs)
	}
	r.Note("id-a", "", true, 1, 1, 2, 0, false)
	r.Save()
	if aggs := r.Snapshot(7); len(aggs) != 1 {
		t.Fatalf("恢复后写入失败: %+v", aggs)
	}
}

// TestNilRecorderSafe 未装配台账（nil）时所有方法可安全调用。
func TestNilRecorderSafe(t *testing.T) {
	var r *Recorder
	r.Note("id", "", true, 1, 1, 2, 0, false)
	r.Save()
	r.Stop()
	if aggs := r.Snapshot(7); aggs != nil {
		t.Fatalf("nil Recorder Snapshot 应为 nil: %+v", aggs)
	}
}

// TestStartStopFlushes Stop 前必须把脏桶落盘（进程优雅退出的最后一道保障）。
func TestStartStopFlushes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-usage.json")
	r := New(path)
	r.Start()
	r.Note("id-a", "", true, 1, 1, 2, 0, false)
	r.Stop()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stop 后应已落盘: %v", err)
	}
}
