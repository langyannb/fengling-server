<?php
// 风铃分享库 - 极简 SMTP 邮件发送 (纯 PHP, 无 composer 依赖)
// 配置写在 config.php:
//   define('SMTP_HOST', 'smtp.qq.com');
//   define('SMTP_PORT', 465);          // 465=SSL
//   define('SMTP_USER', 'xxx@qq.com');
//   define('SMTP_PASS', '授权码');      // 不是登录密码
//   define('SMTP_FROM_NAME', '风铃分享库');

class Mailer
{
    /** 发送 HTML 邮件; 失败返回 false 并把原因写进 PHP error_log */
    public static function send(string $to, string $subject, string $html): bool
    {
        if (!filter_var($to, FILTER_VALIDATE_EMAIL)) {
            error_log('[Mailer] 收件人邮箱不合法: ' . $to);
            return false;
        }
        $host     = defined('SMTP_HOST') ? SMTP_HOST : 'smtp.qq.com';
        $port     = defined('SMTP_PORT') ? (int)SMTP_PORT : 465;
        $user     = defined('SMTP_USER') ? SMTP_USER : '';
        $pass     = defined('SMTP_PASS') ? SMTP_PASS : '';
        $fromName = defined('SMTP_FROM_NAME') ? SMTP_FROM_NAME : '风铃分享库';
        if ($user === '' || $pass === '') {
            error_log('[Mailer] 未配置 SMTP_USER / SMTP_PASS');
            return false;
        }

        $ctx = stream_context_create(['ssl' => [
            'verify_peer' => true, 'verify_peer_name' => true, 'SNI_enabled' => true,
        ]]);
        $fp = @stream_socket_client(
            ($port === 465 ? 'ssl://' : 'tcp://') . $host . ':' . $port,
            $errno, $errstr, 20, STREAM_CLIENT_CONNECT, $ctx
        );
        if (!$fp) {
            error_log("[Mailer] 连接失败 $host:$port ($errno) $errstr");
            return false;
        }
        stream_set_timeout($fp, 20);

        // 读一行/多行 SMTP 响应 ("250-xxx" 续行, 空格结束)
        $read = function () use ($fp): string {
            $data = '';
            while (($line = fgets($fp, 1024)) !== false) {
                $data .= $line;
                if (isset($line[3]) && $line[3] === ' ') break;
            }
            return $data;
        };
        $cmd = function (string $c, array $expect) use ($fp, $read) {
            fwrite($fp, $c . "\r\n");
            $r = $read();
            return in_array((int)substr($r, 0, 3), $expect, true) ? $r : false;
        };
        $close = function () use ($fp) { @fclose($fp); };

        $r = $read();
        if ((int)substr($r, 0, 3) !== 220) { error_log('[Mailer] 问候失败: ' . trim($r)); $close(); return false; }

        $ehlo = $_SERVER['SERVER_NAME'] ?? (gethostname() ?: 'localhost');
        if ($cmd('EHLO ' . $ehlo, [250]) === false)             { error_log('[Mailer] EHLO 失败'); $close(); return false; }
        if ($cmd('AUTH LOGIN', [334]) === false)                { error_log('[Mailer] AUTH LOGIN 被拒'); $close(); return false; }
        if ($cmd(base64_encode($user), [334]) === false)        { error_log('[Mailer] 用户名被拒'); $close(); return false; }
        if ($cmd(base64_encode($pass), [235]) === false)        { error_log('[Mailer] 授权码被拒 (检查 SMTP_PASS)'); $close(); return false; }
        if ($cmd('MAIL FROM:<' . $user . '>', [250]) === false) { error_log('[Mailer] MAIL FROM 失败'); $close(); return false; }
        if ($cmd('RCPT TO:<' . $to . '>', [250, 251]) === false){ error_log('[Mailer] RCPT TO 失败: ' . $to); $close(); return false; }
        if ($cmd('DATA', [354]) === false)                      { error_log('[Mailer] DATA 被拒'); $close(); return false; }

        $data = implode("\r\n", [
            'From: ' . self::encode($fromName) . ' <' . $user . '>',
            'To: <' . $to . '>',
            'Subject: ' . self::encode($subject),
            'Date: ' . date('r'),
            'Message-ID: <' . bin2hex(random_bytes(12)) . '@' . $host . '>',
            'MIME-Version: 1.0',
            'Content-Type: text/html; charset=UTF-8',
            'Content-Transfer-Encoding: base64',
            '',
            chunk_split(base64_encode($html), 76, "\r\n"),
            '.',
        ]);
        fwrite($fp, $data . "\r\n");
        $r = $read();
        if ((int)substr($r, 0, 3) !== 250) { error_log('[Mailer] 发送失败: ' . trim($r)); $close(); return false; }
        @$cmd('QUIT', [221, 250]);
        $close();
        return true;
    }

    /** 验证码邮件 (purpose: register | reset) */
    public static function sendCode(string $to, string $code, string $purpose = 'register'): bool
    {
        $title = $purpose === 'reset' ? '重置密码' : '注册账号';
        $html = '<div style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;max-width:480px;margin:0 auto;padding:24px;">'
            . '<h2 style="color:#4C6FFF;margin:0 0 8px;">风铃分享库</h2>'
            . '<p style="color:#666;margin:0 0 24px;">你正在' . $title . '，验证码 5 分钟内有效。</p>'
            . '<div style="background:#F4F6FF;border-radius:12px;padding:20px;text-align:center;font-size:32px;font-weight:700;letter-spacing:8px;color:#4C6FFF;">' . $code . '</div>'
            . '<p style="color:#999;font-size:13px;margin:20px 0 0;">如果这不是你本人的操作，请忽略这封邮件。</p>'
            . '</div>';
        return self::send($to, '【风铃分享库】验证码 ' . $code, $html);
    }

    private static function encode(string $s): string
    {
        return '=?UTF-8?B?' . base64_encode($s) . '?=';
    }
}
