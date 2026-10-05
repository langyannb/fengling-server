// Package mailer 复刻 mailer.php 的 Mailer 类（极简 SMTP，纯标准库，无第三方依赖）。
//
// 关键事实（2026-10-05 逐行核对）：仓库里的 config.php **没有**定义
// SMTP_USER / SMTP_PASS，所以线上 PHP 的 Mailer::send 在第一道检查就 return false，
// send_code / email_verify_send 固定返回「邮件发送失败, 请稍后重试」。
// Go 侧必须保持同样的默认行为：SMTP_USER/SMTP_PASS 为空 => 直接返回 false，
// 一个字节都不发（配置改由 /etc/fengling/goapi.env 的 SMTP_* 注入，父 agent 可控）。
package mailer

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpfilter"
)

// ioTimeout 对齐 mailer.php 的 stream_set_timeout($fp, 20) 与 20 秒连接超时。
const ioTimeout = 20 * time.Second

// Config 是 SMTP 连接参数（对齐 mailer.php 里 defined() 的默认值）。
type Config struct {
	Host     string // smtp.qq.com
	Port     int    // 465 = 隐式 SSL
	User     string // 为空 = 不发送
	Pass     string // 为空 = 不发送
	FromName string // 风铃分享库
}

// Client 是 SMTP 客户端。
type Client struct {
	cfg  Config
	log  *slog.Logger
	ehlo string
}

// New 构造客户端（纯内存，不建连接）。
func New(cfg Config, log *slog.Logger) *Client {
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = "smtp.qq.com"
	}
	if cfg.Port <= 0 {
		cfg.Port = 465
	}
	if strings.TrimSpace(cfg.FromName) == "" {
		cfg.FromName = "风铃分享库"
	}
	// 对齐 `$_SERVER['SERVER_NAME'] ?? (gethostname() ?: 'localhost')`：
	// Go 侧拿不到 FPM 的 SERVER_NAME，用主机名（EHLO 名对投递没有语义影响）。
	ehlo := ""
	if h, err := os.Hostname(); err == nil {
		ehlo = strings.TrimSpace(h)
	}
	if ehlo == "" {
		ehlo = "localhost"
	}
	return &Client{cfg: cfg, log: log, ehlo: ehlo}
}

// SendCodeTitle 对齐 mailer.php:94 的 purpose → 标题映射。
func SendCodeTitle(purpose string) string {
	switch purpose {
	case "reset":
		return "重置密码"
	case "verify":
		return "验证邮箱"
	default:
		return "注册账号"
	}
}

// SendCodeHTML 逐字复刻 mailer.php:95-100 的邮件正文。
func SendCodeHTML(code, purpose string) string {
	title := SendCodeTitle(purpose)
	return `<div style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;max-width:480px;margin:0 auto;padding:24px;">` +
		`<h2 style="color:#4C6FFF;margin:0 0 8px;">风铃分享库</h2>` +
		`<p style="color:#666;margin:0 0 24px;">你正在` + title + `，验证码 5 分钟内有效。</p>` +
		`<div style="background:#F4F6FF;border-radius:12px;padding:20px;text-align:center;font-size:32px;font-weight:700;letter-spacing:8px;color:#4C6FFF;">` + code + `</div>` +
		`<p style="color:#999;font-size:13px;margin:20px 0 0;">如果这不是你本人的操作，请忽略这封邮件。</p>` +
		`</div>`
}

// SendCode 对齐 Mailer::sendCode($to, $code, $purpose): bool。
func (c *Client) SendCode(to, code, purpose string) bool {
	return c.Send(to, "【风铃分享库】验证码 "+code, SendCodeHTML(code, purpose))
}

// Send 对齐 Mailer::send($to, $subject, $html): bool。
func (c *Client) Send(to, subject, html string) bool {
	if !phpfilter.ValidateEmail(to) {
		c.warn("收件人邮箱不合法", "to", to)
		return false
	}
	if c.cfg.User == "" || c.cfg.Pass == "" {
		// 与 PHP 完全一致：没配账号直接失败（线上现状就是这条路径）。
		c.warn("未配置 SMTP_USER / SMTP_PASS")
		return false
	}
	conn, err := c.dial()
	if err != nil {
		c.warn("连接失败", "host", c.cfg.Host, "port", c.cfg.Port, "err", err)
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)

	// read 复刻 mailer.php 的 $read：读到「第 4 个字符是空格」的行为止（250-xxx 续行）。
	read := func() string {
		var sb strings.Builder
		for {
			line, err := br.ReadString('\n')
			sb.WriteString(line)
			if err != nil {
				return sb.String()
			}
			if len(line) >= 4 && line[3] == ' ' {
				return sb.String()
			}
		}
	}
	cmd := func(text string, expect ...int) bool {
		if _, err := bw.WriteString(text + "\r\n"); err != nil {
			return false
		}
		if err := bw.Flush(); err != nil {
			return false
		}
		return respCodeIn(read(), expect...)
	}

	if r := read(); !respCodeIn(r, 220) {
		c.warn("问候失败", "resp", strings.TrimSpace(r))
		return false
	}
	if !cmd("EHLO "+c.ehlo, 250) {
		c.warn("EHLO 失败")
		return false
	}
	if !cmd("AUTH LOGIN", 334) {
		c.warn("AUTH LOGIN 被拒")
		return false
	}
	if !cmd(base64.StdEncoding.EncodeToString([]byte(c.cfg.User)), 334) {
		c.warn("用户名被拒")
		return false
	}
	if !cmd(base64.StdEncoding.EncodeToString([]byte(c.cfg.Pass)), 235) {
		c.warn("授权码被拒 (检查 SMTP_PASS)")
		return false
	}
	if !cmd("MAIL FROM:<"+c.cfg.User+">", 250) {
		c.warn("MAIL FROM 失败")
		return false
	}
	if !cmd("RCPT TO:<"+to+">", 250, 251) {
		c.warn("RCPT TO 失败", "to", to)
		return false
	}
	if !cmd("DATA", 354) {
		c.warn("DATA 被拒")
		return false
	}

	body := strings.Join([]string{
		"From: " + encodeHeader(c.cfg.FromName) + " <" + c.cfg.User + ">",
		"To: <" + to + ">",
		"Subject: " + encodeHeader(subject),
		// PHP 的 date('r')：本地时区（Asia/Shanghai）的 RFC 2822 形态。
		"Date: " + time.Now().In(config.LocalZone()).Format(time.RFC1123Z),
		"Message-ID: <" + randomHex(12) + "@" + c.cfg.Host + ">",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
		"",
		chunkSplit(base64.StdEncoding.EncodeToString([]byte(html)), 76, "\r\n"),
		".",
	}, "\r\n")
	if _, err := bw.WriteString(body + "\r\n"); err != nil {
		return false
	}
	if err := bw.Flush(); err != nil {
		return false
	}
	if r := read(); !respCodeIn(r, 250) {
		c.warn("发送失败", "resp", strings.TrimSpace(r))
		return false
	}
	_ = cmd("QUIT", 221, 250)
	return true
}

func (c *Client) dial() (net.Conn, error) {
	addr := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	d := &net.Dialer{Timeout: ioTimeout}
	if c.cfg.Port == 465 {
		// 对齐 mailer.php：verify_peer=true + SNI（不做任何降级跳过校验）。
		return tls.DialWithDialer(d, "tcp", addr, &tls.Config{
			ServerName: c.cfg.Host,
			MinVersion: tls.VersionTLS12,
		})
	}
	return d.Dial("tcp", addr)
}

func (c *Client) warn(msg string, args ...any) {
	if c.log != nil {
		c.log.Warn("Mailer: "+msg, args...)
	}
}

// respCode 复刻 `(int)substr($r, 0, 3)`。
func respCode(r string) int {
	s := r
	if len(s) > 3 {
		s = s[:3]
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func respCodeIn(r string, expect ...int) bool {
	got := respCode(r)
	for _, e := range expect {
		if got == e {
			return true
		}
	}
	return false
}

// chunkSplit 复刻 PHP chunk_split($s, $n, $sep)：每 n 个字符插一个分隔符，末尾也补一个。
func chunkSplit(s string, n int, sep string) string {
	if s == "" || n <= 0 {
		return ""
	}
	var sb strings.Builder
	for len(s) > 0 {
		k := n
		if len(s) < k {
			k = len(s)
		}
		sb.WriteString(s[:k])
		sb.WriteString(sep)
		s = s[k:]
	}
	return sb.String()
}

// encodeHeader 对齐 Mailer::encode（=?UTF-8?B?...?=）。
func encodeHeader(s string) string {
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}
