package mailer

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// 进程内假 SMTP 服务器：按 mailer.php 的对话顺序应答，并回收 DATA 段正文。
func startSMTP(t *testing.T, wantUser, wantPass string) (host string, port int, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(portStr)
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		say := func(s string) { _, _ = fmt.Fprintf(conn, "%s\r\n", s) }
		say("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				got <- data.String()
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					say("250 queued")
					continue
				}
				data.WriteString(line)
				data.WriteString("\n")
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				say("250-fake")
				say("250 AUTH LOGIN")
			case line == "AUTH LOGIN":
				say("334 VXNlcm5hbWU6")
			case line == base64.StdEncoding.EncodeToString([]byte(wantUser)):
				say("334 UGFzc3dvcmQ6")
			case line == base64.StdEncoding.EncodeToString([]byte(wantPass)):
				say("235 auth ok")
			case strings.HasPrefix(line, "MAIL FROM:"):
				say("250 ok")
			case strings.HasPrefix(line, "RCPT TO:"):
				say("250 ok")
			case line == "DATA":
				inData = true
				say("354 send data")
			case line == "QUIT":
				say("221 bye")
				got <- data.String()
				return
			default:
				say("250 ok")
			}
		}
	}()
	return host, port, got
}

func TestSendCodeDialogueAndBody(t *testing.T) {
	host, port, got := startSMTP(t, "bot@example.com", "s3cret")
	c := New(Config{Host: host, Port: port, User: "bot@example.com", Pass: "s3cret", FromName: "风铃分享库"}, discardLogger())
	if !c.SendCode("user@example.com", "012345", "register") {
		t.Fatal("SendCode 应成功")
	}
	select {
	case raw := <-got:
		if !strings.Contains(raw, "Subject: "+encodeHeader("【风铃分享库】验证码 012345")) {
			t.Fatalf("主题不对:\n%s", raw)
		}
		if !strings.Contains(raw, "To: <user@example.com>") {
			t.Fatalf("收件人不对:\n%s", raw)
		}
		if !strings.Contains(raw, "Content-Transfer-Encoding: base64") {
			t.Fatalf("缺 base64 传输编码:\n%s", raw)
		}
		html := decodeBase64Body(t, raw)
		if !strings.Contains(html, "你正在注册账号，验证码 5 分钟内有效。") {
			t.Fatalf("正文文案不对:\n%s", html)
		}
		if !strings.Contains(html, ">012345</div>") {
			t.Fatalf("正文里没有验证码:\n%s", html)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没收到邮件正文")
	}
}

// decodeBase64Body 取出 DATA 段里 base64 编码的 HTML 正文（去掉 SMTP 的折行）。
func decodeBase64Body(t *testing.T, raw string) string {
	t.Helper()
	// 服务器把 CRLF 归一成 LF 存储，所以这里按空行切头/体。
	idx := strings.Index(raw, "\n\n")
	if idx < 0 {
		t.Fatalf("找不到头与体之间的空行: %q", raw)
	}
	body := strings.TrimSuffix(raw[idx+2:], "\n")
	body = strings.ReplaceAll(body, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("正文不是合法 base64: %v / %q", err, body)
	}
	return string(decoded)
}
