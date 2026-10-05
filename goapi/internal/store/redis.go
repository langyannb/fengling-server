package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/redis/go-redis/v9"
)

// Redis 封装。三条不可越线的纪律（生产上这个实例是**共用**的）：
//
//  1. 生产 127.0.0.1:6379 上还跑着另一个站点的 WordPress 对象缓存
//     （dbsize≈4188，key 形如 `WPOPT_0.41394300 ...:post-queries:...`），
//     且 `maxmemory 0` + `maxmemory-policy noeviction`。
//     所以：**绝不 FLUSHDB/FLUSHALL**、**绝不 KEYS**（要扫用 SCAN）、只允许库 0。
//  2. 所有键都带统一前缀 `fls:`（唯一例外是「共用实例里别人的键」，我们不碰），
//     并且**所有键都必须有 TTL**，避免 noeviction 下把内存占满。
//  3. Redis 不可用时全部退化：不 panic、不阻塞业务请求（fail-open）。
type Redis struct {
	client  *redis.Client
	enabled bool
	log     *slog.Logger

	mu       sync.Mutex
	lastWarn map[string]time.Time
}

// 全部键/频道都带 fls: 前缀；只有常量集中在这里定义，禁止在别处拼裸键。
const (
	redisKeyOnline  = "fls:online"    // 在线用户集合（有 TTL，随心跳续期）
	redisKeyOnlineP = "fls:online:%d" // 单用户在线标记 SETEX(TTL)
	redisKeyRateLim = "fls:rl:%s:%d"  // 限流：kind 在前、uid 在后（如 fls:rl:upload:42）

	// ChannelEvents 是阶段 3/4 Pub/Sub 的频道名（本单只提供封装，不接管投递）。
	ChannelEvents = "fls:events"

	// 阶段 2：send_code 的「同一邮箱同一用途刚发过」标记（TTL = PHP 的 60 秒窗口）。
	// 键里只放邮箱的 SHA-256 前 16 字节，避免把用户邮箱明文写进共用 Redis。
	redisKeyCodeSent = "fls:rl:send_code:%s:%s"

	// 视频清理的「最近一次完成时间」。
	//
	// 键名冲突说明：阶段 1 契约 §6 明确要求写 `goapi:video:last_cleanup`（供 /healthz
	// 展示），而阶段 0 合同附录 A 要求「所有键必须带 fls: 前缀」。两处都写、都带 TTL
	// （一次 10 分钟的任务，多一条 SETEX 的代价可忽略），这样两边观察口径都能读到。
	redisKeyVideoCleanup    = "goapi:video:last_cleanup"
	redisKeyVideoCleanupFls = "fls:video:last_cleanup"

	videoCleanupTTL = 24 * time.Hour

	redisOpTimeout = 500 * time.Millisecond
	warnEvery      = 30 * time.Second
)

// NewRedis 构造客户端。这里**不 Ping**：Redis 挂了不能拖慢 goapi 启动。
func NewRedis(cfg config.Config, log *slog.Logger) *Redis {
	r := &Redis{log: log, lastWarn: make(map[string]time.Time)}
	if cfg.RedisAddr == "" {
		log.Warn("REDIS_ADDR 为空, Redis 相关能力全部降级")
		return r
	}
	// 共用实例：只允许库 0（不许为了「图省事」切库）。
	db := cfg.RedisDB
	if db != 0 {
		log.Warn("REDIS_DB 非 0, 共用实例上只允许库 0, 已强制回退", "want", db)
		db = 0
	}
	r.client = redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DB:           0,
		DialTimeout:  redisOpTimeout,
		ReadTimeout:  redisOpTimeout,
		WriteTimeout: redisOpTimeout,
		MaxRetries:   0,
	})
	r.enabled = true
	return r
}

// Enabled 表示「配置了 Redis」（不代表此刻连得上）。
func (r *Redis) Enabled() bool { return r != nil && r.enabled && r.client != nil }

// Close 关闭客户端。
func (r *Redis) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}

// Ping 探活（health action 用）。
func (r *Redis) Ping(ctx context.Context) error {
	if !r.Enabled() {
		return errRedisDisabled
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	return r.client.Ping(ctx).Err()
}

var errRedisDisabled = fmt.Errorf("redis 未启用")

func (r *Redis) opCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, redisOpTimeout)
}

// warn 做日志节流：Redis 挂掉时不能每个请求都刷一行日志。
func (r *Redis) warn(op string, err error) {
	if r == nil || r.log == nil {
		return
	}
	now := time.Now()
	r.mu.Lock()
	last, seen := r.lastWarn[op]
	quiet := seen && now.Sub(last) < warnEvery
	if !quiet {
		r.lastWarn[op] = now
	}
	r.mu.Unlock()
	if !quiet {
		r.log.Warn("Redis 操作失败, 功能降级", "op", op, "err", err)
	}
}

// ---------- 在线状态 ----------

// MarkOnline：SADD fls:online <uid> + SETEX fls:online:<uid> <ttl> <now>，
// 并给集合本身续一个 TTL（noeviction 的共用实例上不允许出现「永不过期的键」）。
func (r *Redis) MarkOnline(ctx context.Context, uid int64, ttl time.Duration) {
	if !r.Enabled() {
		return
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	pipe := r.client.Pipeline()
	pipe.SAdd(ctx, redisKeyOnline, uid)
	pipe.Expire(ctx, redisKeyOnline, setTTL(ttl))
	pipe.Set(ctx, fmt.Sprintf(redisKeyOnlineP, uid), time.Now().Unix(), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		r.warn("MarkOnline", err)
	}
}

// RefreshOnline 心跳续期（每 10 秒调一次）。
func (r *Redis) RefreshOnline(ctx context.Context, uid int64, ttl time.Duration) {
	if !r.Enabled() {
		return
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	pipe := r.client.Pipeline()
	pipe.Set(ctx, fmt.Sprintf(redisKeyOnlineP, uid), time.Now().Unix(), ttl)
	pipe.Expire(ctx, redisKeyOnline, setTTL(ttl))
	if _, err := pipe.Exec(ctx); err != nil {
		r.warn("RefreshOnline", err)
	}
}

// MarkOffline 断开时删 key 并 SREM。
func (r *Redis) MarkOffline(ctx context.Context, uid int64) {
	if !r.Enabled() {
		return
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	pipe := r.client.Pipeline()
	pipe.Del(ctx, fmt.Sprintf(redisKeyOnlineP, uid))
	pipe.SRem(ctx, redisKeyOnline, uid)
	if _, err := pipe.Exec(ctx); err != nil {
		r.warn("MarkOffline", err)
	}
}

// OnlineCount 在线用户数（降级时返回 0）。
// 注意：进程被 SIGKILL 时 SREM 来不及执行，集合里可能残留「僵尸成员」；
// TTL 会兜住集合本身，真正的判在线要用 IsOnline（它看的是带 TTL 的单用户键）。
func (r *Redis) OnlineCount(ctx context.Context) int {
	if !r.Enabled() {
		return 0
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	n, err := r.client.SCard(ctx, redisKeyOnline).Result()
	if err != nil {
		r.warn("OnlineCount", err)
		return 0
	}
	return int(n)
}

// IsOnline 某个用户是否在线（降级时返回 false）。
func (r *Redis) IsOnline(ctx context.Context, uid int64) bool {
	if !r.Enabled() {
		return false
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	n, err := r.client.Exists(ctx, fmt.Sprintf(redisKeyOnlineP, uid)).Result()
	if err != nil {
		r.warn("IsOnline", err)
		return false
	}
	return n > 0
}

// setTTL 给集合键留一点余量：用户键 ttl 到期后集合再留 3 倍时间（心跳会持续续期）。
func setTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 90 * time.Second
	}
	return ttl * 3
}

// ---------- 限流（阶段 3/4 用；本单只提供封装 + 单测） ----------

// rateLimitKey 生成限流键，形如 `fls:rl:<kind>:<uid>`。
func rateLimitKey(uid int64, kind string) string {
	return fmt.Sprintf(redisKeyRateLim, kind, uid)
}

// rateLimitExceeded 判断本次计数是否已超限（纯函数，便于单测）。
func rateLimitExceeded(count, limit int64) bool { return count > limit }

// Allow 是「INCR + EXPIRE」计数限流，键一定带 TTL。
// 返回 true = 放行。**Redis 不可用时一律放行（fail-open）**，避免基础设施抖动把业务打死。
func (r *Redis) Allow(ctx context.Context, uid int64, kind string, limit int, window time.Duration) bool {
	if !r.Enabled() || limit <= 0 {
		return true
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	key := rateLimitKey(uid, kind)
	n, err := r.client.Incr(ctx, key).Result()
	if err != nil {
		r.warn("Allow/INCR", err)
		return true
	}
	if n == 1 {
		if err := r.client.Expire(ctx, key, window).Err(); err != nil {
			r.warn("Allow/EXPIRE", err)
		}
	}
	return !rateLimitExceeded(n, int64(limit))
}

// ---------- Pub/Sub（阶段 3/4 接管事件投递；本单只提供封装） ----------

// Publish 向频道发布一条消息（频道名必须带 fls: 前缀，见 ChannelEvents）。
func (r *Redis) Publish(ctx context.Context, channel string, payload []byte) error {
	if !r.Enabled() {
		return errRedisDisabled
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	if err := r.client.Publish(ctx, channel, payload).Err(); err != nil {
		r.warn("Publish", err)
		return err
	}
	return nil
}

// Subscribe 订阅若干频道；Redis 未启用时返回 nil（调用方需判空）。
func (r *Redis) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	if !r.Enabled() || len(channels) == 0 {
		return nil
	}
	return r.client.Subscribe(ctx, channels...)
}

// Clients 返回 go-redis 原始类型，留给阶段 3/4 扩展（本单不直接使用）。
func (r *Redis) Clients() *redis.Client {
	if !r.Enabled() {
		return nil
	}
	return r.client
}

// ---------- 视频清理时间戳 ----------

// SetLastCleanup 记录本次清理完成时间（unix 秒），两个前缀都写、都带 24h TTL。
// Redis 挂了只记日志，绝不返回错误打断清理任务。
func (r *Redis) SetLastCleanup(ctx context.Context, at time.Time, ttl time.Duration) {
	if !r.Enabled() {
		return
	}
	if ttl <= 0 {
		ttl = videoCleanupTTL
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	val := at.Unix()
	pipe := r.client.Pipeline()
	pipe.Set(ctx, redisKeyVideoCleanup, val, ttl)
	pipe.Set(ctx, redisKeyVideoCleanupFls, val, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		r.warn("SetLastCleanup", err)
	}
}

// LastCleanup 读最近一次清理时间（unix 秒）；没有记录或 Redis 不可用时返回 (0,false)。
func (r *Redis) LastCleanup(ctx context.Context) (int64, bool) {
	if !r.Enabled() {
		return 0, false
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	n, err := r.client.Get(ctx, redisKeyVideoCleanup).Int64()
	if err != nil {
		if err != redis.Nil {
			r.warn("LastCleanup", err)
		}
		return 0, false
	}
	return n, true
}

// ---------- 阶段 2：send_code 的 60 秒窗口（同语义加速） ----------

// codeSentKey 生成 `fls:rl:send_code:<purpose>:<sha256(email) 前 16 字节 hex>`。
func codeSentKey(email, purpose string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(email)))
	return fmt.Sprintf(redisKeyCodeSent, purpose, hex.EncodeToString(sum[:16]))
}

// MarkCodeSent 在验证码行成功入库后打一个带 TTL 的标记。
//
// 语义边界（很重要）：这只是**加速**，不参与判定真值。
// 命中缓存只可能发生在「DB 里同样存在 60 秒内的行」时（标记紧跟在 INSERT 之后写），
// 所以短路给「发送太频繁」不会改变结果；任何未命中/Redis 挂掉都回落 DB 查询。
// Redis 不可用时直接返回（fail-open），接口绝不因此 500。
func (r *Redis) MarkCodeSent(ctx context.Context, email, purpose string, ttl time.Duration) {
	if !r.Enabled() {
		return
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	if err := r.client.Set(ctx, codeSentKey(email, purpose), time.Now().Unix(), ttl).Err(); err != nil {
		r.warn("MarkCodeSent", err)
	}
}

// CodeSentRecently 判断是否刚发过（命中 = 等价于 DB 也会拒绝）。
func (r *Redis) CodeSentRecently(ctx context.Context, email, purpose string) bool {
	if !r.Enabled() {
		return false
	}
	ctx, cancel := r.opCtx(ctx)
	defer cancel()
	n, err := r.client.Exists(ctx, codeSentKey(email, purpose)).Result()
	if err != nil {
		r.warn("CodeSentRecently", err)
		return false
	}
	return n > 0
}
