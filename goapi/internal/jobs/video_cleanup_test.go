package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

const fakePublicPrefix = "https://fenglin.example.com/"

type fakeStore struct {
	mu        sync.Mutex
	cfg       store.VideoConfig
	rows      []store.VideoRow
	queryErr  error
	markErr   error
	marks     []string
	queries   int
	panicOnce bool
	panicked  bool
}

func (f *fakeStore) VideoConfig(context.Context) store.VideoConfig { return f.cfg }

func (f *fakeStore) QueryVideoRows(context.Context) ([]store.VideoRow, error) {
	f.mu.Lock()
	f.queries++
	if f.panicOnce && !f.panicked {
		f.panicked = true
		f.mu.Unlock()
		panic("模拟 MySQL 驱动 panic")
	}
	f.mu.Unlock()
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func (f *fakeStore) MarkVideoCleaned(_ context.Context, table string, id int64) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.mu.Lock()
	f.marks = append(f.marks, table+"/"+itoa(id))
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries
}

type fakeObject struct {
	mu      sync.Mutex
	deleted []string
	failFor string
}

func (f *fakeObject) KeyFromURL(u string) string {
	if !strings.HasPrefix(u, fakePublicPrefix) {
		return ""
	}
	return strings.TrimPrefix(u, fakePublicPrefix)
}

func (f *fakeObject) Delete(_ context.Context, key string) error {
	if f.failFor == key {
		return errors.New("模拟 S3 失败")
	}
	f.mu.Lock()
	f.deleted = append(f.deleted, key)
	f.mu.Unlock()
	return nil
}

func (f *fakeObject) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [24]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func row(id int64, sizeMB int, at string) store.VideoRow {
	return store.VideoRow{
		Table:     "social_messages",
		ID:        id,
		Video:     fakePublicPrefix + "chat/v" + itoa(id) + ".mp4",
		Size:      int64(sizeMB) * 1024 * 1024,
		CreatedAt: at,
	}
}

func TestPlanCleanupCapacityDeletesOldest(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rows := []store.VideoRow{
		row(1, 100, "2026-09-01 00:00:00"),
		row(2, 100, "2026-09-02 00:00:00"),
		row(3, 100, "2026-09-03 00:00:00"),
	}
	cfg := store.VideoConfig{MaxMB: 100, TotalLimitMB: 250, KeepDays: 0, AutoClean: 1}
	doomed, res := PlanCleanup(rows, cfg, true, true, true, now)
	if res.Visited != 3 || res.Skipped != "" {
		t.Fatalf("res=%+v", res)
	}
	if len(doomed) != 1 || doomed[0] != 0 {
		t.Fatalf("doomed=%v, 期望只删最旧的第 0 行", doomed)
	}
}

func TestPlanCleanupCapacityNoOpWhenUnderLimit(t *testing.T) {
	now := time.Now()
	rows := []store.VideoRow{row(1, 100, "2026-09-01 00:00:00")}
	cfg := store.VideoConfig{TotalLimitMB: 600, AutoClean: 1}
	if doomed, _ := PlanCleanup(rows, cfg, true, true, true, now); len(doomed) != 0 {
		t.Fatalf("不该删: %v", doomed)
	}
}

func TestPlanCleanupByDays(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rows := []store.VideoRow{
		row(1, 10, "2026-09-20 00:00:00"), // 15 天前 → 超期
		row(2, 10, "2026-10-04 00:00:00"), // 1 天前 → 保留
		row(3, 10, ""),                    // created_at 解析失败 → 不按天数删
	}
	cfg := store.VideoConfig{TotalLimitMB: 600, KeepDays: 7, AutoClean: 1}
	doomed, _ := PlanCleanup(rows, cfg, true, true, true, now)
	if len(doomed) != 1 || doomed[0] != 0 {
		t.Fatalf("doomed=%v", doomed)
	}
	// keep_days=0 时按天数不删
	cfg.KeepDays = 0
	if doomed, _ := PlanCleanup(rows, cfg, true, true, true, now); len(doomed) != 0 {
		t.Fatalf("keep_days=0 不该按天数删: %v", doomed)
	}
}

func TestPlanCleanupRespectsAutoCleanOff(t *testing.T) {
	now := time.Now()
	rows := []store.VideoRow{row(1, 1000, "2020-01-01 00:00:00")}
	cfg := store.VideoConfig{TotalLimitMB: 100, KeepDays: 1, AutoClean: 0}
	doomed, res := PlanCleanup(rows, cfg, true, true, true, now)
	if len(doomed) != 0 {
		t.Fatalf("auto_clean=0 不该删: %v", doomed)
	}
	if res.Skipped != "auto_clean=0 只统计不删除" {
		t.Fatalf("skipped=%q", res.Skipped)
	}
	if res.Visited != 1 {
		t.Fatalf("visited=%d", res.Visited)
	}
	// respectAutoClean=false（后台手动清理）时照删
	if doomed, _ := PlanCleanup(rows, cfg, true, true, false, now); len(doomed) != 1 {
		t.Fatalf("respectAutoClean=false 应删: %v", doomed)
	}
}

func TestPlanCleanupEmptyRows(t *testing.T) {
	doomed, res := PlanCleanup(nil, store.VideoConfig{AutoClean: 0}, true, true, true, time.Now())
	if doomed != nil || res.Visited != 0 || res.Skipped != "" {
		t.Fatalf("doomed=%v res=%+v", doomed, res)
	}
}

func TestParseCreatedAtUsesShanghaiZone(t *testing.T) {
	got := parseCreatedAt("2026-10-05 20:00:00")
	want := time.Date(2026, 10, 5, 20, 0, 0, 0, config.LocalZone()).Unix()
	if got != want {
		t.Fatalf("got=%d want=%d", got, want)
	}
	if parseCreatedAt("not a date") != 0 {
		t.Fatal("非法时间应返回 0")
	}
}

func TestCleanupFullPipeline(t *testing.T) {
	fs := &fakeStore{
		cfg: store.VideoConfig{TotalLimitMB: 250, AutoClean: 1},
		rows: []store.VideoRow{
			row(1, 100, "2026-09-01 00:00:00"),
			row(2, 100, "2026-09-02 00:00:00"),
			row(3, 100, "2026-09-03 00:00:00"),
			{Table: "social_pm_messages", ID: 9, Video: "https://other.example.com/x.mp4", Size: 5, CreatedAt: "2026-09-04 00:00:00"},
		},
	}
	// 第 3 行(非本站 URL)不产生删除键
	obj := &fakeObject{}
	c := NewVideoCleaner(CleanerDeps{Store: fs, Object: obj, Log: quiet()})

	res := c.Cleanup(context.Background())
	if res.Deleted != 1 || res.FreedBytes != 100*1024*1024 || res.Visited != 4 {
		t.Fatalf("res=%+v", res)
	}
	if keys := obj.deletedKeys(); len(keys) != 1 || keys[0] != "chat/v1.mp4" {
		t.Fatalf("deleted=%v", keys)
	}
	if len(fs.marks) != 1 || fs.marks[0] != "social_messages/1" {
		t.Fatalf("marks=%v", fs.marks)
	}
}

func TestCleanupCountsNothingWhenMarkFails(t *testing.T) {
	fs := &fakeStore{
		cfg:     store.VideoConfig{TotalLimitMB: 1, AutoClean: 1},
		rows:    []store.VideoRow{row(1, 10, "2026-09-01 00:00:00")},
		markErr: errors.New("模拟 UPDATE 失败"),
	}
	c := NewVideoCleaner(CleanerDeps{Store: fs, Object: &fakeObject{}, Log: quiet()})
	res := c.Cleanup(context.Background())
	if res.Deleted != 0 || res.FreedBytes != 0 {
		t.Fatalf("改库失败不能计 deleted: %+v", res)
	}
}

func TestCleanupDegradesWhenQueryFails(t *testing.T) {
	fs := &fakeStore{cfg: store.VideoConfig{TotalLimitMB: 1, AutoClean: 1}, queryErr: errors.New("模拟 MySQL 挂了")}
	c := NewVideoCleaner(CleanerDeps{Store: fs, Object: &fakeObject{}, Redis: nil, Log: quiet()})
	res := c.Cleanup(context.Background())
	if res.Deleted != 0 {
		t.Fatalf("MySQL 挂了不该报错也不该删: %+v", res)
	}
}

// 常驻任务：能跑、能停，且 Stop 之后 Run 立刻退出。
func TestTickerRunAndStop(t *testing.T) {
	fs := &fakeStore{cfg: store.VideoConfig{TotalLimitMB: 1, AutoClean: 1}, rows: []store.VideoRow{row(1, 10, "")}}
	c := NewVideoCleaner(CleanerDeps{
		Store: fs, Object: &fakeObject{}, Log: quiet(),
		Cfg: config.Config{VideoCleanupInitial: 5 * time.Millisecond, VideoCleanupInterval: 5 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for fs.queryCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if fs.queryCount() < 2 {
		t.Fatalf("ticker 没有按周期重复执行: queries=%d", fs.queryCount())
	}

	done := make(chan struct{})
	go func() { c.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 未能在 2 秒内返回")
	}
	// 幂等：再调一次也不能挂
	c.Stop()

	// ctx 取消也能干净退出
	c2 := NewVideoCleaner(CleanerDeps{Store: fs, Object: &fakeObject{}, Log: quiet(),
		Cfg: config.Config{VideoCleanupInitial: time.Hour, VideoCleanupInterval: time.Hour}})
	ctx2, cancel2 := context.WithCancel(context.Background())
	go c2.Run(ctx2)
	time.Sleep(20 * time.Millisecond)
	cancel2()
	c2.Stop()
}

// 单次执行 panic 不得拖垮 ticker 或进程。
func TestTickerSurvivesPanic(t *testing.T) {
	fs := &fakeStore{
		cfg:       store.VideoConfig{TotalLimitMB: 1, AutoClean: 1},
		rows:      []store.VideoRow{row(1, 10, "")},
		panicOnce: true,
	}
	c := NewVideoCleaner(CleanerDeps{
		Store: fs, Object: &fakeObject{}, Log: quiet(),
		Cfg: config.Config{VideoCleanupInitial: 5 * time.Millisecond, VideoCleanupInterval: 5 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for fs.queryCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if fs.queryCount() < 2 {
		t.Fatalf("panic 之后 ticker 没有再跑: queries=%d", fs.queryCount())
	}
	c.Stop()
}

// Run 之前就 Stop：Run 必须立刻返回，Stop 也不能卡住。
func TestStopBeforeRun(t *testing.T) {
	fs := &fakeStore{cfg: store.VideoConfig{AutoClean: 1}}
	c := NewVideoCleaner(CleanerDeps{Store: fs, Object: &fakeObject{}, Log: quiet()})
	c.Stop()
	done := make(chan struct{})
	go func() { c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop 之后 Run 应立即返回")
	}
	if fs.queryCount() != 0 {
		t.Fatalf("不该跑清理: %d", fs.queryCount())
	}
}
