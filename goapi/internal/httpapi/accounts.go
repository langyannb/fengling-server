package httpapi

import (
	"context"
	"errors"

	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// accountsDB 是阶段 2「账号与鉴权」action 需要的数据访问原语。
//
// 为什么做成窄接口（而不是直接调 *store.Store）：单测必须在不连真实 MySQL 的
// 前提下覆盖 current_user 的两条查询、登录成功/失败/封禁三分支、注册的逐条校验
// 与「验证码答错也作废」等路径。用一个脚本化假实现注入这些行，比跑真库或引入
// sqlmock 依赖更简单，也更容易断言「发出去的 SQL 就是 PHP 的那一条」。
// *store.Store 天然满足本接口；New() 在 env.Accounts 为空时自动补 env.Store。
type accountsDB interface {
	// CurrentUserRow 对齐 current_user()：先 sessions JOIN users，再回落 users.token。
	CurrentUserRow(ctx context.Context, token string) (store.Row, error)
	QueryRow(ctx context.Context, query string, args ...any) (store.Row, error)
	QueryAll(ctx context.Context, query string, args ...any) ([]store.Row, error)
	// QueryValue 取第一行第一列（对齐 PDO fetchColumn()），用于 COUNT(*)/MAX(id) 这类
	// 不写别名的查询，保证 SQL 文本与 PHP 一致。
	QueryValue(ctx context.Context, query string, args ...any) (any, bool, error)
	Exec(ctx context.Context, query string, args ...any) (store.ExecResult, error)
}

// codeMailer 是验证码邮件发送（对齐 mailer.php 的 Mailer::sendCode）。
// 单测注入假实现；为空时一律当作「发送失败」（与线上没配 SMTP_USER/SMTP_PASS 一致）。
type codeMailer interface {
	SendCode(to, code, purpose string) bool
}

var errAccountsUnavailable = errors.New("数据访问层未初始化")

// accounts 返回数据访问实现。理论上 New() 已经补过默认值；这里再兜一层，
// 避免任何一个 handler 因 nil 接口 panic（宁可回 500 错误响应）。
func (rt *Router) accounts() accountsDB {
	if rt.env.Accounts != nil {
		return rt.env.Accounts
	}
	if rt.env.Store != nil {
		return rt.env.Store
	}
	return unavailableAccounts{}
}

// sendCodeMail 调邮件发送；Mailer 为空 = 发送失败（不会 panic）。
func (rt *Router) sendCodeMail(to, code, purpose string) bool {
	if rt.env.Mailer == nil {
		return false
	}
	return rt.env.Mailer.SendCode(to, code, purpose)
}

// unavailableAccounts 是「Store 都没配」时的哨兵：所有操作返回同一个错误。
type unavailableAccounts struct{}

func (unavailableAccounts) CurrentUserRow(context.Context, string) (store.Row, error) {
	return nil, errAccountsUnavailable
}
func (unavailableAccounts) QueryRow(context.Context, string, ...any) (store.Row, error) {
	return nil, errAccountsUnavailable
}
func (unavailableAccounts) QueryAll(context.Context, string, ...any) ([]store.Row, error) {
	return nil, errAccountsUnavailable
}
func (unavailableAccounts) QueryValue(context.Context, string, ...any) (any, bool, error) {
	return nil, false, errAccountsUnavailable
}
func (unavailableAccounts) Exec(context.Context, string, ...any) (store.ExecResult, error) {
	return store.ExecResult{}, errAccountsUnavailable
}

// HasParam 对齐 api.php:3339 has_param()：只看「这个键被提交过没有」。
//
// 与 Param 的区别（照抄 PHP 的两个不同语义）：
//   - has_param 用 array_key_exists，**JSON 里显式传 null 也算提交过**；
//   - param 用 isset，显式 null 会继续往 $_POST/$_GET 回落。
//
// 所以 user_update 这种「留空不改」的接口用的是本方法。
func (c *Ctx) HasParam(key string) bool {
	if b := c.Body(); b != nil {
		if _, ok := b[key]; ok {
			return true
		}
	}
	if f := c.PostForm(); f != nil {
		if _, ok := f[key]; ok {
			return true
		}
	}
	if c.mp != nil {
		if _, ok := c.mp[key]; ok {
			return true
		}
	}
	_, ok := c.Query[key]
	return ok
}

// paramAny 取原始参数值（保留 PHP param() 的「字符串 vs 数字」差异）。
func (c *Ctx) paramAny(key string) any {
	v, _ := c.Param(key)
	return v
}

// dbError 是数据库异常的统一出口：细节只进日志，返回给客户端的是 500 + 原因。
// 绝不带 Go 的文件名/行号（PHP 也不对外暴露路径；README 的硬规矩）。
func (c *Ctx) dbError(err error) {
	c.Log().Error("数据库操作失败", "err", err)
	c.Error("服务器错误: "+err.Error(), 500)
}
