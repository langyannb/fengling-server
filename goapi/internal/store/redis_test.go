package store

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Redis 未启用（本机没有 Redis）时**所有方法都必须安全返回**，绝不 panic。
// 这正是生产上「Redis 挂了要降级不崩」的第一道保证。
func TestRedisDegradedWhenDisabled(t *testing.T) {
	cfg := config.Load()
	cfg.RedisAddr = "" // 显式禁用
	r := NewRedis(cfg, quietLogger())
	if r.Enabled() {
		t.Fatal("空地址不应启用")
	}
	ctx := context.Background()
	r.MarkOnline(ctx, 1, time.Minute)
	r.RefreshOnline(ctx, 1, time.Minute)
	r.MarkOffline(ctx, 1)
	if n := r.OnlineCount(ctx); n != 0 {
		t.Fatalf("OnlineCount=%d", n)
	}
	if r.IsOnline(ctx, 1) {
		t.Fatal("IsOnline 应为 false")
	}
	if !r.Allow(ctx, 1, "upload", 1, time.Minute) {
		t.Fatal("限流必须 fail-open（放行）")
	}
	if err := r.Publish(ctx, ChannelEvents, []byte("x")); err == nil {
		t.Fatal("Publish 应返回错误（未启用）")
	}
	if ps := r.Subscribe(ctx, ChannelEvents); ps != nil {
		t.Fatal("Subscribe 应返回 nil")
	}
	if err := r.Ping(ctx); err == nil {
		t.Fatal("Ping 应返回错误")
	}
	if r.Clients() != nil {
		t.Fatal("Clients 应返回 nil")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close 应安全: %v", err)
	}
}

// 连不上的地址也不能 panic：所有方法仍然安全返回（fail-open）。
func TestRedisUnreachableDoesNotPanic(t *testing.T) {
	cfg := config.Load()
	cfg.RedisAddr = "127.0.0.1:1" // 必然连不上
	r := NewRedis(cfg, quietLogger())
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r.MarkOnline(ctx, 7, 90*time.Second)
	r.RefreshOnline(ctx, 7, 90*time.Second)
	r.MarkOffline(ctx, 7)
	if n := r.OnlineCount(ctx); n != 0 {
		t.Fatalf("OnlineCount=%d", n)
	}
	if r.IsOnline(ctx, 7) {
		t.Fatal("IsOnline 应为 false")
	}
	if !r.Allow(ctx, 7, "post", 5, time.Minute) {
		t.Fatal("限流必须 fail-open")
	}
	if err := r.Ping(ctx); err == nil {
		t.Fatal("Ping 应报错")
	}
}

// 共用 Redis 实例的纪律：键/频道必须带 fls: 前缀（唯一例外是 fls:online 本身）。
func TestRedisKeysAreNamespacedAndTTLable(t *testing.T) {
	if !strings.HasPrefix(redisKeyOnline, "fls:") {
		t.Fatalf("redisKeyOnline=%s", redisKeyOnline)
	}
	if !strings.HasPrefix(redisKeyOnlineP, "fls:") {
		t.Fatalf("redisKeyOnlineP=%s", redisKeyOnlineP)
	}
	if !strings.HasPrefix(redisKeyRateLim, "fls:") {
		t.Fatalf("redisKeyRateLim=%s", redisKeyRateLim)
	}
	if !strings.HasPrefix(ChannelEvents, "fls:") {
		t.Fatalf("ChannelEvents=%s", ChannelEvents)
	}
	if got := rateLimitKey(42, "upload"); got != "fls:rl:upload:42" {
		t.Fatalf("rateLimitKey=%s", got)
	}
	// 集合键必须有 TTL（noeviction 的共用实例上不允许永不过期的键）
	if got := setTTL(90 * time.Second); got != 270*time.Second {
		t.Fatalf("setTTL=%v", got)
	}
	if got := setTTL(0); got != 90*time.Second {
		t.Fatalf("setTTL(0)=%v", got)
	}
}

func TestRateLimitExceeded(t *testing.T) {
	cases := []struct {
		count, limit int64
		want         bool
	}{
		{1, 3, false},
		{3, 3, false}, // 第 3 次仍放行
		{4, 3, true},  // 第 4 次拒绝
		{0, 1, false},
	}
	for _, c := range cases {
		if got := rateLimitExceeded(c.count, c.limit); got != c.want {
			t.Errorf("rateLimitExceeded(%d,%d)=%v want %v", c.count, c.limit, got, c.want)
		}
	}
}

// 非 0 库会被强制回退到 0（共用实例只允许库 0）。
func TestRedisForcesDB0(t *testing.T) {
	cfg := config.Load()
	cfg.RedisAddr = "127.0.0.1:6379"
	cfg.RedisDB = 3
	r := NewRedis(cfg, quietLogger())
	defer r.Close()
	if !r.Enabled() {
		t.Fatal("应启用")
	}
	if r.Clients() == nil {
		t.Fatal("client 不应为 nil")
	}
	if opts := r.Clients().Options(); opts.DB != 0 {
		t.Fatalf("DB=%d, 期望 0", opts.DB)
	}
}
