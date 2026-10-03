<?php
// 风铃分享库 - API 入口
// 所有接口统一走这里: /api.php?action=xxx
// 抑制 PHP 8.x deprecated 警告 (imagedestroy/curl_close 等无效果但会污染 JSON 响应)
error_reporting(E_ALL & ~E_DEPRECATED & ~E_NOTICE & ~E_WARNING);
// GD 解码大图按 宽*高*4 字节占内存, 默认 128M 上传大图会 OOM 致命错误 -> 前端拿到 HTML 报「响应解析失败」
@ini_set('memory_limit', '512M');
// 把 PHP 致命错误也转成 JSON, 前端永远拿到 {code,msg} 而不是错误页
register_shutdown_function(function () {
    $e = error_get_last();
    if (!$e || !in_array($e['type'], [E_ERROR, E_PARSE, E_CORE_ERROR, E_COMPILE_ERROR], true)) return;
    if (headers_sent()) return;
    http_response_code(500);
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode([
        'code' => -1,
        'msg'  => '服务器错误: ' . $e['message'] . ' @ ' . basename($e['file']) . ':' . $e['line'],
    ], JSON_UNESCAPED_UNICODE);
});
require_once __DIR__ . '/config.php';
require_once __DIR__ . '/uc.php';
require_once __DIR__ . '/apk.php';
require_once __DIR__ . '/mailer.php';

header('Access-Control-Allow-Origin: *');
header('Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS');
header('Access-Control-Allow-Headers: Content-Type, Authorization');

if ($_SERVER['REQUEST_METHOD'] === 'OPTIONS') {
    http_response_code(204);
    exit;
}

$action = $_GET['action'] ?? '';



// ============ 雨云对象存储 (S3 兼容) ============
define('S3_ENDPOINT', 'https://cn-nb1.rains3.com');
define('S3_BUCKET', 'fenglin');
define('S3_REGION', 'cn-nb1');
define('S3_ACCESS_KEY', 'qLvix4DJFzd4DS1n');
define('S3_SECRET_KEY', 'ENhWei361GTC752TsoayUlhcb7svyn');
// 图形验证码使用的 TrueType 字体 (找不到时自动退回内置字体)
if (!defined('CAPTCHA_FONT')) define('CAPTCHA_FONT', '/usr/share/fonts/truetype/lato/Lato-Bold.ttf');

define('S3_PUBLIC_URL', 'https://fenglin.cn-nb1.rains3.com'); // 公共读地址

/** HMAC-SHA256 辅助 */
function s3_hmac(string $key, string $msg): string {
    return hash_hmac('sha256', $msg, $key, true);
}

/** 生成 AWS SigV4 签名头 */
function s3_sign_v4(string $method, string $path, string $payloadHash): array {
    $now = gmdate('Ymd\THis\Z');
    $dateStamp = gmdate('Ymd');
    $host = parse_url(S3_ENDPOINT, PHP_URL_HOST);
    $canonicalHeaders = "host:" . $host . "\nx-amz-content-sha256:" . $payloadHash . "\nx-amz-date:" . $now . "\n";
    $signedHeaders = "host;x-amz-content-sha256;x-amz-date";
    $canonicalRequest = $method . "\n" . $path . "\n\n" . $canonicalHeaders . "\n" . $signedHeaders . "\n" . $payloadHash;
    $scope = $dateStamp . "/" . S3_REGION . "/s3/aws4_request";
    $stringToSign = "AWS4-HMAC-SHA256\n" . $now . "\n" . $scope . "\n" . hash('sha256', $canonicalRequest);
    $kDate = s3_hmac("AWS4" . S3_SECRET_KEY, $dateStamp);
    $kRegion = s3_hmac($kDate, S3_REGION);
    $kService = s3_hmac($kRegion, "s3");
    $kSigning = s3_hmac($kService, "aws4_request");
    $signature = hash_hmac('sha256', $stringToSign, $kSigning);
    return [
        "Authorization: AWS4-HMAC-SHA256 Credential=" . S3_ACCESS_KEY . "/" . $scope .
            ", SignedHeaders=" . $signedHeaders . ", Signature=" . $signature,
        "x-amz-date: " . $now,
        "x-amz-content-sha256: " . $payloadHash,
    ];
}

/**
 * 上传文件到雨云 S3 (SigV4 + curl)
 * @param string $localPath 本地文件路径
 * @param string $key 对象键 (如 images/xxx.png)
 * @param string $contentType MIME 类型
 * @return bool 是否成功
 */
function s3_upload(string $localPath, string $key, string $contentType = 'application/octet-stream'): bool {
    if (!file_exists($localPath)) return false;
    $body = file_get_contents($localPath);
    if ($body === false) return false;
    $payloadHash = hash('sha256', $body);
    $path = "/" . S3_BUCKET . "/" . $key;
    $headers = s3_sign_v4('PUT', $path, $payloadHash);
    $headers[] = "Content-Type: " . $contentType;
    $ch = curl_init(S3_ENDPOINT . $path);
    curl_setopt_array($ch, [
        CURLOPT_CUSTOMREQUEST => 'PUT',
        CURLOPT_POSTFIELDS => $body,
        CURLOPT_HTTPHEADER => $headers,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CONNECTTIMEOUT => 15,
        CURLOPT_TIMEOUT => 120,
    ]);
    $resp = curl_exec($ch);
    $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    return $code >= 200 && $code < 300;
}

/** 上传原始字节到 S3 (内存直接上传, 不落盘) */
function s3_upload_bytes(string $data, string $key, string $contentType = 'application/octet-stream'): bool {
    $payloadHash = hash('sha256', $data);
    $path = "/" . S3_BUCKET . "/" . $key;
    $headers = s3_sign_v4('PUT', $path, $payloadHash);
    $headers[] = "Content-Type: " . $contentType;
    $ch = curl_init(S3_ENDPOINT . $path);
    curl_setopt_array($ch, [
        CURLOPT_CUSTOMREQUEST => 'PUT',
        CURLOPT_POSTFIELDS => $data,
        CURLOPT_HTTPHEADER => $headers,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CONNECTTIMEOUT => 15,
        CURLOPT_TIMEOUT => 300,
    ]);
    $resp = curl_exec($ch);
    $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    return $code >= 200 && $code < 300;
}

/** 删除 S3 对象 */
function s3_delete(string $key): bool {
    $payloadHash = hash('sha256', '');
    $path = "/" . S3_BUCKET . "/" . $key;
    $headers = s3_sign_v4('DELETE', $path, $payloadHash);
    $ch = curl_init(S3_ENDPOINT . $path);
    curl_setopt_array($ch, [
        CURLOPT_CUSTOMREQUEST => 'DELETE',
        CURLOPT_HTTPHEADER => $headers,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CONNECTTIMEOUT => 15,
        CURLOPT_TIMEOUT => 60,
    ]);
    $resp = curl_exec($ch);
    $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    return $code >= 200 && $code < 300;
}

/** 生成对象键: 目录/日期_随机.ext */
function s3_key(string $dir, string $ext): string {
    return $dir . "/" . date('YmdHis') . "_" . bin2hex(random_bytes(4)) . "." . $ext;
}

/** 图片压缩 (GD): 压到 maxWidth 宽内 + JPEG 质量 80, 返回压缩后字节 */
function compress_image(string $data, string $ext, int $maxWidth = 1600, int $quality = 80): string {
    // 保护: 先读图片头部尺寸, 超过 6000 万像素不解码 (原样上传), 避免 GD 吃掉几百 MB 内存
    $info = @getimagesizefromstring($data);
    if ($info && !empty($info[0]) && !empty($info[1]) && ($info[0] * $info[1]) > 60000000) return $data;
    $img = @imagecreatefromstring($data);
    if (!$img) return $data; // 无法解析, 原样返回
    $w = imagesx($img);
    $h = imagesy($img);
    if ($w > $maxWidth) {
        $nh = (int)round($h * $maxWidth / $w);
        $dst = imagecreatetruecolor($maxWidth, $nh);
        // 保持透明 (png/webp)
        imagealphablending($dst, false);
        imagesavealpha($dst, true);
        imagecopyresampled($dst, $img, 0, 0, 0, 0, $maxWidth, $nh, $w, $h);
        $img = $dst;
    }
    ob_start();
    if (in_array($ext, ['jpg', 'jpeg'])) {
        imagejpeg($img, null, $quality);
    } elseif ($ext === 'webp') {
        imagewebp($img, null, $quality);
    } else { // png/gif: 保持格式, png 压缩级别
        imagepng($img, null, 7);
    }
    $out = ob_get_clean();
    @imagedestroy($img);
    return $out ?: $data;
}

try {
    switch ($action) {
        // ============ 认证 ============
        case 'login':
            // 支持用「用户名」或「邮箱」登录
            $username = trim(param('username', ''));
            $password = param('password', '');
            if (!$username || !$password) json_error('用户名和密码不能为空');
            $stmt = db()->prepare("SELECT * FROM users WHERE username = ? OR (email <> '' AND email = ?)");
            $stmt->execute([$username, $username]);
            $user = $stmt->fetch();
            if (!$user || !password_verify($password, $user['password'])) {
                json_error('用户名或密码错误');
            }
            if ((int)$user['is_active'] !== 1) json_error('账号已被封禁, 请联系管理员', 403);
            $token = make_token();
            // 多会话: 每个登录会话独立 token, 不覆盖 users.token (多设备登录互不顶掉)
            db()->prepare('UPDATE users SET token = ? WHERE id = ?')->execute([$token, $user['id']]);
            try { db()->prepare('INSERT INTO sessions (user_id, token) VALUES (?, ?)')->execute([$user['id'], $token]); } catch (Exception $e) {}
            try { db()->prepare('UPDATE users SET last_login_at = NOW() WHERE id = ?')->execute([$user['id']]); } catch (Exception $e) {}
            json_out([
                'token' => $token,
                'user'  => user_public($user),
            ]);

        // ============ 邮箱验证码 ============
        // 限流: 同邮箱同用途 60 秒 1 条 / 同邮箱每天 10 条 / 同 IP 每小时 20 条
        case 'send_code':
            $email   = trim((string)param('email', ''));
            $purpose = param('purpose', 'register') === 'reset' ? 'reset' : 'register';
            if (!captcha_check((string)param('captcha_token', ''), (string)param('captcha_code', ''))) {
                json_error('图形验证码错误或已过期');
            }
            if (!filter_var($email, FILTER_VALIDATE_EMAIL)) json_error('邮箱格式不正确');
            $st = db()->prepare('SELECT id FROM users WHERE email = ?');
            $st->execute([$email]);
            $exists = (bool)$st->fetch();
            if ($purpose === 'register' && $exists) json_error('该邮箱已被注册');
            if ($purpose === 'reset' && !$exists) json_error('该邮箱尚未注册');
            $st = db()->prepare('SELECT created_at FROM email_codes WHERE email = ? AND purpose = ? ORDER BY id DESC LIMIT 1');
            $st->execute([$email, $purpose]);
            $last = $st->fetchColumn();
            if ($last && time() - strtotime($last) < 60) json_error('发送太频繁, 请 60 秒后再试');
            $st = db()->prepare('SELECT COUNT(*) FROM email_codes WHERE email = ? AND purpose = ? AND created_at > DATE_SUB(NOW(), INTERVAL 1 DAY)');
            $st->execute([$email, $purpose]);
            if ((int)$st->fetchColumn() >= 10) json_error('今日发送次数已达上限');
            $ip = client_ip();
            $st = db()->prepare('SELECT COUNT(*) FROM email_codes WHERE ip = ? AND created_at > DATE_SUB(NOW(), INTERVAL 1 HOUR)');
            $st->execute([$ip]);
            if ((int)$st->fetchColumn() >= 20) json_error('操作过于频繁, 请稍后再试');
            $code = str_pad((string)random_int(0, 999999), 6, '0', STR_PAD_LEFT);
            db()->prepare('INSERT INTO email_codes (email, code, purpose, ip, expires_at) VALUES (?, ?, ?, ?, DATE_ADD(NOW(), INTERVAL 5 MINUTE))')
                ->execute([$email, $code, $purpose, $ip]);
            if (!Mailer::sendCode($email, $code, $purpose)) {
                error_log('[send_code] 邮件发送失败: ' . $email);
                json_error('邮件发送失败, 请稍后重试');
            }
            json_out(['expires_in' => 300]);

        // ============ 注册 ============
        case 'register':
            $username = trim((string)param('username', ''));
            $password = (string)param('password', '');
            $nickname = trim((string)param('nickname', ''));
            $email    = trim((string)param('email', ''));
            $code     = trim((string)param('code', ''));
            if (!preg_match('/^[A-Za-z0-9_]{3,20}$/', $username)) json_error('用户名只能包含字母数字下划线(3-20位)');
            if (strlen($password) < 6) json_error('密码至少 6 位');
            if (!filter_var($email, FILTER_VALIDATE_EMAIL)) json_error('邮箱格式不正确');
            if ($nickname === '') $nickname = $username;
            if (mb_strlen($nickname) > 20) json_error('昵称不能超过 20 个字');
            $st = db()->prepare('SELECT id FROM users WHERE username = ?');
            $st->execute([$username]);
            if ($st->fetch()) json_error('用户名已存在');
            $st = db()->prepare('SELECT id FROM users WHERE email = ?');
            $st->execute([$email]);
            if ($st->fetch()) json_error('该邮箱已被注册');
            $st = db()->prepare("SELECT id, code, tries FROM email_codes WHERE email = ? AND purpose = 'register' AND used = 0 AND expires_at > NOW() ORDER BY id DESC LIMIT 1");
            $st->execute([$email]);
            $row = $st->fetch();
            if (!$row) json_error('验证码错误或已过期');
            if ((int)$row['tries'] >= 5) json_error('验证码错误次数过多, 请重新获取');
            if (!hash_equals((string)$row['code'], $code)) {
                db()->prepare('UPDATE email_codes SET tries = tries + 1 WHERE id = ?')->execute([$row['id']]);
                json_error('验证码错误或已过期');
            }
            db()->prepare('UPDATE email_codes SET used = 1 WHERE id = ?')->execute([$row['id']]);
            $emailVal = $email === '' ? null : $email;
            db()->prepare("INSERT INTO users (username, password, nickname, email, email_verified, role, last_login_at) VALUES (?, ?, ?, ?, 1, 'user', NOW())")
                ->execute([$username, password_hash($password, PASSWORD_DEFAULT), $nickname, $emailVal]);
            $uid = (int)db()->lastInsertId();
            $token = make_token();
            // 注册即登录: token 必须落库, 否则返回的 token 是孤儿, 后续鉴权全部 401
            db()->prepare('UPDATE users SET token = ? WHERE id = ?')->execute([$token, $uid]);
            try { db()->prepare('INSERT INTO sessions (user_id, token) VALUES (?, ?)')->execute([$uid, $token]); } catch (Exception $e) {}
            $st = db()->prepare('SELECT * FROM users WHERE id = ?');
            $st->execute([$uid]);
            $newUser = $st->fetch();
            // 注册成功推送欢迎通知, 让「我的 - 消息」有初始内容
            notify_push($uid, '欢迎加入风铃分享库', "你好, " . $nickname . "!\n账号已创建成功, 邮箱已验证。\n可到「我的 - 社交」参与群组聊天, 有问题欢迎在群里反馈。", 'system');
            json_out(['token' => $token, 'user' => user_public($newUser)]);

        // ============ 我的账号 (需登录) ============
        case 'user_me':
            json_out(user_public(current_user_or_401()));

        case 'user_update':
            $me = current_user_or_401();
            $fields = [];
            $args = [];
            if (has_param('nickname')) {
                $nickname = trim((string)param('nickname', ''));
                if ($nickname === '') json_error('昵称不能为空');
                if (mb_strlen($nickname) > 20) json_error('昵称不能超过 20 个字');
                $fields[] = 'nickname = ?'; $args[] = $nickname;
            }
            if (has_param('bio')) {
                $bio = trim((string)param('bio', ''));
                if (mb_strlen($bio) > 100) json_error('简介不能超过 100 个字');
                $fields[] = 'bio = ?'; $args[] = $bio;
            }
            if (has_param('avatar')) {
                $avatar = trim((string)param('avatar', ''));
                if ($avatar !== '' && !preg_match('#^https?://#i', $avatar)) json_error('头像地址不合法');
                $fields[] = 'avatar = ?'; $args[] = $avatar;
            }
            if (!$fields) json_error('没有需要更新的内容');
            $args[] = $me['id'];
            db()->prepare('UPDATE users SET ' . implode(', ', $fields) . ' WHERE id = ?')->execute($args);
            $st = db()->prepare('SELECT * FROM users WHERE id = ?');
            $st->execute([$me['id']]);
            json_out(user_public($st->fetch()));

        // 头像上传: multipart, 字段名 file; 压到 512 宽后传对象存储
        case 'user_avatar':
            $me = current_user_or_401();
            if (empty($_FILES['file'])) json_error('未收到文件');
            $file = $_FILES['file'];
            if ((int)($file['size'] ?? 0) > 10 * 1024 * 1024) json_error('头像不能超过 10MB');
            $raw = @file_get_contents($file['tmp_name']);
            if ($raw === false || $raw === '') json_error('读取文件失败');
            $info = @getimagesizefromstring($raw);
            if (!$info) json_error('这不是一张有效的图片');
            // 以真实图片内容推断扩展名: 相册选图 (Android GetContent) 的文件名常常没有扩展名
            $mimeMap = ['image/jpeg' => 'jpg', 'image/png' => 'png', 'image/gif' => 'gif', 'image/webp' => 'webp'];
            $realMime = strtolower((string)($info['mime'] ?? ''));
            if (!isset($mimeMap[$realMime])) json_error('只支持 jpg / png / gif / webp 图片');
            $ext = $mimeMap[$realMime];
            $raw = compress_image($raw, $ext, 512, 90);
            $mime = in_array($ext, ['jpg', 'jpeg']) ? 'image/jpeg'
                : ($ext === 'png' ? 'image/png' : ($ext === 'webp' ? 'image/webp' : 'image/gif'));
            $key = s3_key('avatars', $ext === 'jpeg' ? 'jpg' : $ext);
            if (!s3_upload_bytes($raw, $key, $mime)) json_error('上传失败, 请稍后重试');
            $url = S3_PUBLIC_URL . '/' . $key;
            db()->prepare('UPDATE users SET avatar = ? WHERE id = ?')->execute([$url, $me['id']]);
            json_out(['url' => $url]);

        case 'user_password':
            $me  = current_user_or_401();
            $old = (string)param('old_password', '');
            $new = (string)param('new_password', '');
            if (strlen($new) < 6) json_error('密码至少 6 位');
            if (!password_verify($old, $me['password'])) json_error('原密码不正确');
            db()->prepare('UPDATE users SET password = ? WHERE id = ?')
                ->execute([password_hash($new, PASSWORD_DEFAULT), $me['id']]);
            json_out(null);

        // ============ 用户管理 (管理员) ============
        case 'admin_users':
            require_admin();
            $page   = max(1, (int)param('page', 1));
            $size   = min(100, max(1, (int)param('page_size', 20)));
            $kw     = trim((string)param('keyword', ''));
            $role   = (string)param('role', '');
            $active = (string)param('is_active', '');
            $where = []; $args = [];
            if ($kw !== '') {
                $where[] = '(username LIKE ? OR nickname LIKE ? OR email LIKE ?)';
                $like = '%' . $kw . '%';
                array_push($args, $like, $like, $like);
            }
            if (in_array($role, ['user', 'admin'], true)) { $where[] = 'role = ?'; $args[] = $role; }
            if ($active === '0' || $active === '1') { $where[] = 'is_active = ?'; $args[] = (int)$active; }
            $wsql = $where ? (' WHERE ' . implode(' AND ', $where)) : '';
            $st = db()->prepare('SELECT COUNT(*) FROM users' . $wsql);
            $st->execute($args);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare('SELECT * FROM users' . $wsql . ' ORDER BY id ASC LIMIT ' . (($page - 1) * $size) . ', ' . $size);
            $st->execute($args);
            json_out([
                'list'      => array_map('user_public', $st->fetchAll()),
                'total'     => $total,
                'page'      => $page,
                'page_size' => $size,
            ]);

        case 'admin_user_save':
            require_admin();
            $self     = current_user_or_401();
            $id       = (int)param('id', 0);
            $username = trim((string)param('username', ''));
            $password = (string)param('password', '');
            $nickname = trim((string)param('nickname', ''));
            $email    = trim((string)param('email', ''));
            $bio      = trim((string)param('bio', ''));
            $role     = param('role', 'user') === 'admin' ? 'admin' : 'user';
            $active   = (int)param('is_active', 1) ? 1 : 0;
            // username 为空 = 未提交该字段 (仅改 is_active 等), 留到 id>0 分支沿用原值后再校验
            if ($username !== '' && !preg_match('/^[A-Za-z0-9_]{3,20}$/', $username)) json_error('用户名只能包含字母数字下划线(3-20位)');
            if ($email !== '' && !filter_var($email, FILTER_VALIDATE_EMAIL)) json_error('邮箱格式不正确');
            if ($nickname === '' && $username !== '') $nickname = $username;
            if (mb_strlen($nickname) > 20) json_error('昵称不能超过 20 个字');
            if (mb_strlen($bio) > 100) json_error('简介不能超过 100 个字');
            $st = db()->prepare('SELECT id FROM users WHERE username = ? AND id <> ?');
            $st->execute([$username, $id]);
            if ($st->fetch()) json_error('用户名已存在');
            if ($email !== '') {
                $st = db()->prepare('SELECT id FROM users WHERE email = ? AND id <> ?');
                $st->execute([$email, $id]);
                if ($st->fetch()) json_error('该邮箱已被注册');
            }
            if ($id > 0) {
                $st = db()->prepare('SELECT * FROM users WHERE id = ?');
                $st->execute([$id]);
                $old = $st->fetch();
                if (!$old) json_error('用户不存在');
                if ((int)$self['id'] === $id && ($role !== 'admin' || $active === 0)) {
                    json_error('不能修改自己的角色, 也不能禁用自己的账号');
                }
                // 未提交的字段沿用原值, 便于只传 id + is_active 做封禁/解封
                if ($username === '') $username = (string)$old['username'];
                if ($nickname === '') $nickname = (string)$old['nickname'];
                if ($email === '') $email = (string)($old['email'] ?? '');
                if ($bio === '') $bio = (string)($old['bio'] ?? '');
                if (!has_param('role')) $role = (string)$old['role'];
                if (!has_param('is_active')) $active = (int)$old['is_active'] ? 1 : 0;
                if ($username === '' || !preg_match('/^[A-Za-z0-9_]{3,20}$/', $username)) {
                    json_error('用户名只能包含字母数字下划线(3-20位)');
                }
                $emailVal = $email === '' ? null : $email;
                $fields = ['username = ?', 'nickname = ?', 'email = ?', 'role = ?', 'is_active = ?', 'bio = ?'];
                $args   = [$username, $nickname, $emailVal, $role, $active, $bio];
                if ($password !== '') {
                    if (strlen($password) < 6) json_error('密码至少 6 位');
                    $fields[] = 'password = ?';
                    $args[]   = password_hash($password, PASSWORD_DEFAULT);
                }
                if ($email !== '' && $email !== (string)$old['email']) {
                    $fields[] = 'email_verified = ?';
                    $args[]   = 0;
                }
                $args[] = $id;
                db()->prepare('UPDATE users SET ' . implode(', ', $fields) . ' WHERE id = ?')->execute($args);
                // 由封禁改为启用时推一条通知
                if ($active === 1 && (int)$old['is_active'] === 0) {
                    notify_push($id, '账号已恢复', "你的账号已由管理员解除封禁, 现在可以正常登录使用了。", 'admin');
                }
            } else {
                if (strlen($password) < 6) json_error('密码至少 6 位');
                $emailVal = $email === '' ? null : $email;
                db()->prepare('INSERT INTO users (username, password, nickname, email, email_verified, role, is_active, bio) VALUES (?, ?, ?, ?, 0, ?, ?, ?)')
                    ->execute([$username, password_hash($password, PASSWORD_DEFAULT), $nickname, $emailVal, $role, $active, $bio]);
                $id = (int)db()->lastInsertId();
            }
            $st = db()->prepare('SELECT * FROM users WHERE id = ?');
            $st->execute([$id]);
            json_out(user_public($st->fetch()));

        case 'admin_user_delete':
            require_admin();
            $self = current_user_or_401();
            $id   = (int)param('id', 0);
            if ($id <= 0) json_error('参数错误');
            if ((int)$self['id'] === $id) json_error('不能删除自己的账号');
            $st = db()->prepare('SELECT role FROM users WHERE id = ?');
            $st->execute([$id]);
            $u = $st->fetch();
            if (!$u) json_error('用户不存在');
            if ($u['role'] === 'admin') {
                $n = (int)db()->query("SELECT COUNT(*) FROM users WHERE role = 'admin' AND is_active = 1")->fetchColumn();
                if ($n <= 1) json_error('至少保留一个管理员');
            }
            db()->prepare('DELETE FROM sessions WHERE user_id = ?')->execute([$id]);
            db()->prepare('DELETE FROM users WHERE id = ?')->execute([$id]);
            json_out(null);

        // ============ 图形验证码 ============
        // 4 位字母数字, 180 秒有效, 一次性; 返回 data:image/png;base64 直接可塞进 img
        case 'captcha':
            $cap = captcha_generate();
            json_out(['token' => $cap[0], 'image' => captcha_image($cap[1]), 'expires_in' => 180]);

        // ============ 邮箱验证 (已登录用户给自己邮箱验证) ============
        case 'email_verify_send':
            $me = current_user_or_401();
            if (!captcha_check((string)param('captcha_token', ''), (string)param('captcha_code', ''))) {
                json_error('图形验证码错误或已过期');
            }
            $email = trim((string)param('email', ''));
            if ($email === '') $email = trim((string)($me['email'] ?? ''));
            if (!filter_var($email, FILTER_VALIDATE_EMAIL)) json_error('邮箱格式不正确');
            $st = db()->prepare('SELECT id FROM users WHERE email = ? AND id <> ?');
            $st->execute([$email, $me['id']]);
            if ($st->fetch()) json_error('该邮箱已被其他账号使用');
            $st = db()->prepare("SELECT created_at FROM email_codes WHERE email = ? AND purpose = 'verify' ORDER BY id DESC LIMIT 1");
            $st->execute([$email]);
            $last = $st->fetchColumn();
            if ($last && time() - strtotime($last) < 60) json_error('发送太频繁, 请 60 秒后再试');
            $ip = client_ip();
            $st = db()->prepare('SELECT COUNT(*) FROM email_codes WHERE ip = ? AND created_at > DATE_SUB(NOW(), INTERVAL 1 HOUR)');
            $st->execute([$ip]);
            if ((int)$st->fetchColumn() >= 20) json_error('操作过于频繁, 请稍后再试');
            $code = str_pad((string)random_int(0, 999999), 6, '0', STR_PAD_LEFT);
            db()->prepare('INSERT INTO email_codes (email, code, purpose, ip, expires_at) VALUES (?, ?, ?, ?, DATE_ADD(NOW(), INTERVAL 5 MINUTE))')
                ->execute([$email, $code, 'verify', $ip]);
            if (!Mailer::sendCode($email, $code, 'verify')) {
                error_log('[email_verify_send] 邮件发送失败: ' . $email);
                json_error('邮件发送失败, 请稍后重试');
            }
            json_out(['ok' => true, 'email' => $email, 'expires_in' => 300]);

        case 'email_verify':
            $me = current_user_or_401();
            $email = trim((string)param('email', ''));
            $code  = trim((string)param('code', ''));
            if (!filter_var($email, FILTER_VALIDATE_EMAIL)) json_error('邮箱格式不正确');
            if ($code === '') json_error('请输入验证码');
            $st = db()->prepare("SELECT id, code, tries FROM email_codes WHERE email = ? AND purpose = 'verify' AND used = 0 AND expires_at > NOW() ORDER BY id DESC LIMIT 1");
            $st->execute([$email]);
            $row = $st->fetch();
            if (!$row) json_error('验证码错误或已过期');
            if ((int)$row['tries'] >= 5) json_error('验证码错误次数过多, 请重新获取');
            if (!hash_equals((string)$row['code'], $code)) {
                db()->prepare('UPDATE email_codes SET tries = tries + 1 WHERE id = ?')->execute([$row['id']]);
                json_error('验证码错误或已过期');
            }
            db()->prepare('UPDATE email_codes SET used = 1 WHERE id = ?')->execute([$row['id']]);
            $st = db()->prepare('SELECT id FROM users WHERE email = ? AND id <> ?');
            $st->execute([$email, $me['id']]);
            if ($st->fetch()) json_error('该邮箱已被其他账号使用');
            db()->prepare('UPDATE users SET email = ?, email_verified = 1 WHERE id = ?')->execute([$email, $me['id']]);
            notify_push((int)$me['id'], '邮箱验证成功', '你的邮箱 ' . $email . ' 已验证成功, 可用于找回密码。', 'system');
            $st = db()->prepare('SELECT * FROM users WHERE id = ?');
            $st->execute([$me['id']]);
            json_out(['user' => user_public($st->fetch())]);

        // ============ 社交群组 ============
        case 'social_groups':
            $rows = db()->query("SELECT g.*,
                    (SELECT COUNT(DISTINCT m.user_id) FROM social_messages m WHERE m.group_id = g.id) AS member_count,
                    (SELECT COUNT(*) FROM social_messages m WHERE m.group_id = g.id AND m.is_recalled = 0) AS message_count
                FROM social_groups g WHERE g.is_active = 1 ORDER BY g.sort_order ASC, g.id ASC")->fetchAll();
            json_out(['list' => array_map('social_group_public', $rows)]);

        case 'social_group':
            $gid = (int)param('id', 0);
            $g = social_group_or_404($gid, false);
            $st = db()->prepare("SELECT DISTINCT u.id, u.nickname, u.username FROM social_messages m INNER JOIN users u ON u.id = m.user_id WHERE m.group_id = ? AND u.role = 'admin'");
            $st->execute([$gid]);
            $st2 = db()->query("SELECT COUNT(DISTINCT user_id) AS c FROM social_messages WHERE group_id = " . $gid);
            $g['member_count'] = (int)($st2->fetch()['c'] ?? 0);
            json_out(['group' => social_group_public($g), 'notice' => (string)($g['notice'] ?? ''), 'admins' => $st->fetchAll()]);

        case 'social_messages':
            $me = current_user_or_401();
            $gid = (int)param('group_id', 0);
            $after = (int)param('after_id', 0);
            $limit = min(50, max(1, (int)param('limit', 30)));
            social_group_or_404($gid, false);
            $sql = "SELECT m.*, u.nickname, u.username, u.avatar, u.role
                    FROM social_messages m LEFT JOIN users u ON u.id = m.user_id
                    WHERE m.group_id = ?";
            if ($after > 0) {
                $st = db()->prepare($sql . ' AND m.id > ? ORDER BY m.id ASC LIMIT ' . $limit);
                $st->execute([$gid, $after]);
                $rows = $st->fetchAll();
            } else {
                $st = db()->prepare($sql . ' ORDER BY m.id DESC LIMIT ' . $limit);
                $st->execute([$gid]);
                $rows = array_reverse($st->fetchAll());
            }
            if ($rows) {
                $ids = [];
                foreach ($rows as $r) { $ids[] = (int)$r['user_id']; }
                $names = [];
                // 注意: 占位符个数必须按「去重后」的 id 数生成, 否则同一人发多条消息时
                // 会出现 SQLSTATE[HY093] number of bound variables does not match number of tokens
                $uniqIds = array_values(array_unique($ids));
                $in = implode(',', array_fill(0, count($uniqIds), '?'));
                $st = db()->prepare('SELECT id, nickname, username FROM users WHERE id IN (' . $in . ')');
                $st->execute($uniqIds);
                foreach ($st->fetchAll() as $u2) {
                    $names[(int)$u2['id']] = $u2['nickname'] !== '' ? $u2['nickname'] : $u2['username'];
                }
                foreach ($rows as $i => $r) { $rows[$i]['at_names'] = $names; }
            }
            json_out(['list' => array_map('social_msg_public', $rows), 'has_more' => count($rows) >= $limit]);

        case 'social_send':
            $me = current_user_or_401();
            if ((int)$me['is_active'] !== 1) json_error('你已被封禁', 403);
            $gid = (int)param('group_id', 0);
            $g = social_group_or_404($gid, false);
            $content = trim((string)param('content', ''));
            if ($content === '') json_error('消息内容不能为空');
            if (mb_strlen($content) > 500) json_error('消息不能超过 500 个字');
            $st = db()->prepare('SELECT created_at FROM social_messages WHERE user_id = ? AND group_id = ? ORDER BY id DESC LIMIT 1');
            $st->execute([(int)$me['id'], $gid]);
            $last = $st->fetchColumn();
            if ($last && strtotime($last) > time() - 2) json_error('发送太快了, 请稍后再试');
            $at = param('at', []);
            if (is_string($at)) $at = preg_split('/[,\s]+/', $at);
            if (!is_array($at)) $at = [];
            $at = array_values(array_unique(array_filter(array_map('intval', $at), function ($v) use ($me) {
                return $v > 0 && $v !== (int)$me['id'];
            })));
            db()->prepare('INSERT INTO social_messages (group_id, user_id, content, at_users, is_recalled) VALUES (?, ?, ?, ?, 0)')
                ->execute([$gid, (int)$me['id'], $content, implode(',', $at)]);
            $mid = (int)db()->lastInsertId();
            if ($at) {
                $in = implode(',', array_fill(0, count($at), '?'));
                $st = db()->prepare('SELECT id FROM users WHERE id IN (' . $in . ') AND is_active = 1');
                $st->execute($at);
                foreach ($st->fetchAll() as $t) {
                    notify_push((int)$t['id'], '有人在「' . $g['name'] . '」@了你', mb_substr($content, 0, 80), 'social');
                }
            }
            json_out(['id' => $mid, 'created_at' => date('Y-m-d H:i:s')]);

        case 'social_recall':
            $me = current_user_or_401();
            $id = (int)param('id', 0);
            $st = db()->prepare('SELECT * FROM social_messages WHERE id = ?');
            $st->execute([$id]);
            $m = $st->fetch();
            if (!$m) json_error('消息不存在');
            if ((int)$m['is_recalled'] === 1) json_error('消息已经撤回了');
            $isAdmin = ($me['role'] ?? '') === 'admin';
            $mine = (int)$m['user_id'] === (int)$me['id'];
            if (!$isAdmin && !$mine) json_error('没有权限撤回这条消息', 403);
            if (!$isAdmin && strtotime($m['created_at']) < time() - 300) json_error('只能撤回 5 分钟内的消息');
            db()->prepare('UPDATE social_messages SET is_recalled = 1, recalled_by = ?, recalled_at = NOW() WHERE id = ?')
                ->execute([(int)$me['id'], $id]);
            json_out(['ok' => true]);

        case 'social_group_notice_set':
            require_admin();
            $gid = (int)param('group_id', 0);
            $g = social_group_or_404($gid, true);
            $notice = trim((string)param('notice', ''));
            if (mb_strlen($notice) > 500) json_error('公告不能超过 500 个字');
            db()->prepare('UPDATE social_groups SET notice = ? WHERE id = ?')->execute([$notice, $gid]);
            json_out(['ok' => true]);

        // 群成员候选 (供客户端 @ 选择): 在该群发过言的活跃用户 + 全部管理员, 排除自己
        case 'social_group_members':
            $me = current_user_or_401();
            $gid = (int)param('group_id', 0);
            social_group_or_404($gid, false);
            $st = db()->prepare("SELECT DISTINCT u.id, u.nickname, u.username, u.avatar, u.role
                    FROM users u
                    WHERE u.is_active = 1 AND u.id <> ? AND (
                        u.role = 'admin'
                        OR EXISTS (SELECT 1 FROM social_messages m WHERE m.user_id = u.id AND m.group_id = ?)
                    )
                    ORDER BY u.role ASC, u.id ASC");
            $st->execute([(int)$me['id'], $gid]);
            $list = [];
            foreach ($st->fetchAll() as $u) {
                $nick = trim((string)($u['nickname'] ?? ''));
                $list[] = [
                    'id' => (int)$u['id'],
                    'nickname' => $nick !== '' ? $nick : (string)$u['username'],
                    'username' => (string)$u['username'],
                    'avatar' => (string)($u['avatar'] ?? ''),
                    'role' => (string)$u['role'],
                ];
            }
            json_out(['list' => $list]);

        // ============ 消息通知 ============
        case 'notifications':
            $me = current_user_or_401();
            $page = max(1, (int)param('page', 1));
            $ps = min(50, max(1, (int)param('page_size', 20)));
            $unreadOnly = (int)param('unread_only', 0) === 1;
            $cond = 'user_id = ?' . ($unreadOnly ? ' AND is_read = 0' : '');
            $st = db()->prepare('SELECT COUNT(*) FROM notifications WHERE ' . $cond);
            $st->execute([(int)$me['id']]);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare('SELECT COUNT(*) FROM notifications WHERE user_id = ? AND is_read = 0');
            $st->execute([(int)$me['id']]);
            $unread = (int)$st->fetchColumn();
            $off = ($page - 1) * $ps;
            $st = db()->prepare('SELECT * FROM notifications WHERE ' . $cond . ' ORDER BY id DESC LIMIT ' . $ps . ' OFFSET ' . $off);
            $st->execute([(int)$me['id']]);
            json_out([
                'list' => array_map('notify_public', $st->fetchAll()),
                'total' => $total, 'unread' => $unread, 'page' => $page, 'page_size' => $ps,
            ]);

        case 'notification_read':
            $me = current_user_or_401();
            if ((int)param('all', 0) === 1) {
                db()->prepare('UPDATE notifications SET is_read = 1 WHERE user_id = ?')->execute([(int)$me['id']]);
            } else {
                $id = (int)param('id', 0);
                if ($id <= 0) json_error('参数错误');
                db()->prepare('UPDATE notifications SET is_read = 1 WHERE id = ? AND user_id = ?')->execute([$id, (int)$me['id']]);
            }
            $st = db()->prepare('SELECT COUNT(*) FROM notifications WHERE user_id = ? AND is_read = 0');
            $st->execute([(int)$me['id']]);
            json_out(['ok' => true, 'unread' => (int)$st->fetchColumn()]);

        case 'notification_delete':
            $me = current_user_or_401();
            $id = (int)param('id', 0);
            if ($id <= 0) json_error('参数错误');
            db()->prepare('DELETE FROM notifications WHERE id = ? AND user_id = ?')->execute([$id, (int)$me['id']]);
            json_out(['ok' => true]);

        // ============ 后台: 群组管理 ============
        case 'admin_groups':
            require_admin();
            $rows = db()->query("SELECT g.*,
                    (SELECT COUNT(DISTINCT m.user_id) FROM social_messages m WHERE m.group_id = g.id) AS member_count,
                    (SELECT COUNT(*) FROM social_messages m WHERE m.group_id = g.id) AS message_count
                FROM social_groups g ORDER BY g.sort_order ASC, g.id ASC")->fetchAll();
            json_out(['list' => array_map('social_group_public', $rows)]);

        case 'admin_group_save':
            require_admin();
            $id = (int)param('id', 0);
            $name = trim((string)param('name', ''));
            // 更新时未提交 name 字段则沿用原值, 避免「一键启用/停用」这类只传部分字段的调用被拒
            if ($id > 0 && !has_param('name')) {
                $stn0 = db()->prepare('SELECT name FROM social_groups WHERE id = ?');
                $stn0->execute([$id]);
                $oldN0 = $stn0->fetch();
                if ($oldN0) $name = (string)($oldN0['name'] ?? '');
            }
            if ($name === '') json_error('群组名称不能为空');
            if (mb_strlen($name) > 20) json_error('群组名称不能超过 20 个字');
            $icon = trim((string)param('icon', ''));
            // 群组头像上传: 表单带 icon_file 文件时优先, 压缩到 256 宽后传对象存储
            if (isset($_FILES['icon_file']) && is_array($_FILES['icon_file']) && (int)($_FILES['icon_file']['error'] ?? 4) === 0) {
                $rawIcon = @file_get_contents((string)($_FILES['icon_file']['tmp_name'] ?? ''));
                if ($rawIcon === false || $rawIcon === '') json_error('头像文件读取失败, 请重试');
                $infoIcon = @getimagesizefromstring($rawIcon);
                if (!$infoIcon || empty($infoIcon['mime'])) json_error('这不是一张有效的图片');
                $mimeIcon = strtolower((string)$infoIcon['mime']);
                $extMapIcon = ['image/jpeg' => 'jpg', 'image/png' => 'png', 'image/gif' => 'gif', 'image/webp' => 'webp'];
                if (!isset($extMapIcon[$mimeIcon])) json_error('只支持 jpg / png / gif / webp 图片');
                $extIcon = $extMapIcon[$mimeIcon];
                $dataIcon = compress_image($rawIcon, $extIcon, 256, 88);
                $keyIcon = s3_key('group_icons', $extIcon);
                if (!s3_upload_bytes($dataIcon, $keyIcon, $mimeIcon)) json_error('头像上传失败, 请稍后重试');
                $icon = S3_PUBLIC_URL . '/' . $keyIcon;
            }
            $desc = trim((string)param('description', ''));
            if (mb_strlen($desc) > 100) json_error('简介不能超过 100 个字');
            $sort = (int)param('sort_order', 0);
            $active = (int)param('is_active', 1) === 1 ? 1 : 0;
            $notice = trim((string)param('notice', ''));
            // 未提交 notice 字段时沿用原公告, 避免「一键启用/停用」把公告清空
            if ($id > 0 && !has_param('notice')) {
                $stn = db()->prepare('SELECT notice FROM social_groups WHERE id = ?');
                $stn->execute([$id]);
                $oldN = $stn->fetch();
                if ($oldN) $notice = (string)($oldN['notice'] ?? '');
            }
            $st = db()->prepare('SELECT id FROM social_groups WHERE name = ? AND id <> ?');
            $st->execute([$name, $id]);
            if ($st->fetch()) json_error('已存在同名群组');
            if ($id > 0) {
                db()->prepare('UPDATE social_groups SET name = ?, icon = ?, description = ?, sort_order = ?, is_active = ?, notice = ? WHERE id = ?')
                    ->execute([$name, $icon, $desc, $sort, $active, $notice, $id]);
                json_out(['id' => $id]);
            }
            db()->prepare('INSERT INTO social_groups (name, icon, description, sort_order, is_active, notice) VALUES (?, ?, ?, ?, ?, ?)')
                ->execute([$name, $icon, $desc, $sort, $active, $notice]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'admin_group_delete':
            require_admin();
            $id = (int)param('id', 0);
            if ($id <= 0) json_error('参数错误');
            db()->prepare('DELETE FROM social_messages WHERE group_id = ?')->execute([$id]);
            db()->prepare('DELETE FROM social_groups WHERE id = ?')->execute([$id]);
            json_out(['ok' => true]);

        // ============ 后台: 群消息管理 ============
        case 'admin_social_messages':
            require_admin();
            $page = max(1, (int)param('page', 1));
            $ps = min(100, max(1, (int)param('page_size', 20)));
            $gid = (int)param('group_id', 0);
            $kw = trim((string)param('keyword', ''));
            $where = []; $args = [];
            if ($gid > 0) { $where[] = 'm.group_id = ?'; $args[] = $gid; }
            if ($kw !== '') { $where[] = 'm.content LIKE ?'; $args[] = '%' . $kw . '%'; }
            $cond = $where ? ('WHERE ' . implode(' AND ', $where)) : '';
            $st = db()->prepare('SELECT COUNT(*) FROM social_messages m ' . $cond);
            $st->execute($args);
            $total = (int)$st->fetchColumn();
            $off = ($page - 1) * $ps;
            $st = db()->prepare("SELECT m.*, u.nickname, u.username, u.avatar, u.role, g.name AS group_name
                FROM social_messages m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN social_groups g ON g.id = m.group_id
                " . $cond . ' ORDER BY m.id DESC LIMIT ' . $ps . ' OFFSET ' . $off);
            $st->execute($args);
            $rows = $st->fetchAll();
            foreach ($rows as $i => $r) {
                $rows[$i]['group_name'] = (string)$r['group_name'];
                $rows[$i]['nickname'] = ($r['nickname'] !== null && $r['nickname'] !== '') ? $r['nickname'] : (string)$r['username'];
            }
            json_out(['list' => array_map('social_msg_public', $rows), 'total' => $total, 'page' => $page, 'page_size' => $ps]);

        case 'admin_social_message_delete':
            require_admin();
            $id = (int)param('id', 0);
            if ($id <= 0) json_error('参数错误');
            db()->prepare('DELETE FROM social_messages WHERE id = ?')->execute([$id]);
            json_out(['ok' => true]);

        // ============ 后台: 通知下发 ============
        case 'admin_notify_send':
            require_admin();
            $title = trim((string)param('title', ''));
            $content = trim((string)param('content', ''));
            $type = param('type', 'admin') === 'system' ? 'system' : 'admin';
            $link = trim((string)param('link', ''));
            if ($title === '') json_error('标题不能为空');
            if ($content === '') json_error('内容不能为空');
            if (mb_strlen($title) > 50) json_error('标题不能超过 50 个字');
            $target = param('target', 'all');
            if ($target === 'all' || $target === '' || (int)$target === 0) {
                $count = notify_push_all($title, $content, $type, $link);
            } else {
                $uid = (int)$target;
                $st = db()->prepare('SELECT id FROM users WHERE id = ?');
                $st->execute([$uid]);
                if (!$st->fetch()) json_error('用户不存在');
                notify_push($uid, $title, $content, $type, $link);
                $count = 1;
            }
            json_out(['ok' => true, 'count' => $count]);

        case 'admin_notification_list':
            require_admin();
            $page = max(1, (int)param('page', 1));
            $ps = min(100, max(1, (int)param('page_size', 20)));
            $kw = trim((string)param('keyword', ''));
            $where = []; $args = [];
            if ($kw !== '') { $where[] = '(n.title LIKE ? OR n.content LIKE ?)'; $args[] = '%' . $kw . '%'; $args[] = '%' . $kw . '%'; }
            $cond = $where ? ('WHERE ' . implode(' AND ', $where)) : '';
            $st = db()->prepare('SELECT COUNT(*) FROM notifications n ' . $cond);
            $st->execute($args);
            $total = (int)$st->fetchColumn();
            $off = ($page - 1) * $ps;
            $st = db()->prepare("SELECT n.*, u.nickname, u.username FROM notifications n LEFT JOIN users u ON u.id = n.user_id
                " . $cond . ' ORDER BY n.id DESC LIMIT ' . $ps . ' OFFSET ' . $off);
            $st->execute($args);
            $rows = $st->fetchAll();
            foreach ($rows as $i => $r) {
                $rows[$i]['user_name'] = ($r['nickname'] !== null && $r['nickname'] !== '') ? $r['nickname'] : (string)$r['username'];
            }
            json_out(['list' => $rows, 'total' => $total, 'page' => $page, 'page_size' => $ps]);

        case 'admin_notification_delete':
            require_admin();
            $id = (int)param('id', 0);
            if ($id <= 0) {
                db()->exec('DELETE FROM notifications');
                json_out(['ok' => true, 'cleared' => true]);
            }
            db()->prepare('DELETE FROM notifications WHERE id = ?')->execute([$id]);
            json_out(['ok' => true]);

        case 'logout':
            $token = param('token', '');
            if ($token) {
                db()->prepare('DELETE FROM sessions WHERE token = ?')->execute([$token]);
                // 仅当 users.token 等于当前 token 才清 (避免顶掉其他会话)
                db()->prepare('UPDATE users SET token = NULL WHERE token = ?')->execute([$token]);
            }
            json_out(null);

        // ============ 分类 (公开读, 管理写) ============
        // parent_id=0 顶级在前, 子分类跟随父分类 (App 端按 parent_id 建树)
        case 'categories':
            json_out(db()->query('SELECT * FROM categories ORDER BY parent_id ASC, sort_order ASC, id ASC')->fetchAll());

        case 'category_create':
            require_admin();
            $name = param('name', '');
            if (!$name) json_error('分类名不能为空');
            $parentId = (int)param('parent_id', 0);
            $color = param('color', '#4C6FFF');
            $icon = param('icon', ''); // 分类图标 (上传图片 URL)
            $sort = (int)param('sort_order', 0);
            if ($parentId > 0 && !category_exists($parentId)) json_error('父分类不存在');
            db()->prepare('INSERT INTO categories (parent_id, name, color, icon, sort_order) VALUES (?, ?, ?, ?, ?)')
                ->execute([$parentId, $name, $color, $icon, $sort]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'category_update':
            require_admin();
            $id = (int)param('id', 0);
            $name = param('name');
            $parentId = param('parent_id', null);
            $color = param('color');
            $icon = param('icon');
            $sort = param('sort_order');
            // 父分类不能指向自己; 不能指向自己的子分类 (防循环)
            if ($parentId !== null && (int)$parentId === $id) json_error('父分类不能是自己');
            $sql = 'UPDATE categories SET ';
            $args = [];
            if ($name !== null) { $sql .= 'name = ?, '; $args[] = $name; }
            if ($parentId !== null) { $sql .= 'parent_id = ?, '; $args[] = (int)$parentId; }
            if ($color !== null) { $sql .= 'color = ?, '; $args[] = $color; }
            if ($icon !== null) { $sql .= 'icon = ?, '; $args[] = $icon; }
            if ($sort !== null) { $sql .= 'sort_order = ?, '; $args[] = (int)$sort; }
            if (!$args) json_error('没有要更新的字段');
            $sql = rtrim($sql, ', ') . ' WHERE id = ?';
            $args[] = $id;
            db()->prepare($sql)->execute($args);
            json_out(null);

        case 'category_delete':
            require_admin();
            $id = (int)param('id', 0);
            // 子分类提升为顶级, 避免孤儿分类
            db()->prepare('UPDATE categories SET parent_id = 0 WHERE parent_id = ?')->execute([$id]);
            db()->prepare('DELETE FROM categories WHERE id = ?')->execute([$id]);
            json_out(null);

        // ============ 软件 (公开读, 管理写) ============
        case 'apps':
            $categoryId = (int)param('category_id', 0);
            $keyword = param('keyword', '');
            $packId = param('pack_id', null);
            // 后台管理需要看到已下架软件 (前端传 include_inactive=1 且必须为管理员); 访客强制只看上架的
            $includeInactive = (int)param('include_inactive', 0) === 1 && is_admin();
            $sql = 'SELECT a.*, c.name AS category_name, c.color AS category_color FROM apps a
                    LEFT JOIN categories c ON a.category_id = c.id
                    WHERE ' . ($includeInactive ? '1 = 1' : 'a.is_active = 1');
            $args = [];
            if ($packId !== null) {
                // 整合包子项: pack_id = 父 id
                $sql .= ' AND a.pack_id = ?';
                $args[] = (int)$packId ?: 0;
            } elseif ($categoryId > 0) {
                // 分类过滤: 父分类 → 包含其所有子分类的软件; 子分类 → 只查自己
                $subs = db()->prepare('SELECT id FROM categories WHERE parent_id = ?');
                $subs->execute([$categoryId]);
                $subIds = array_column($subs->fetchAll(), 'id');
                if ($subIds) {
                    $subIds[] = $categoryId;
                    $sql .= ' AND a.category_id IN (' . implode(',', array_map('intval', $subIds)) . ')';
                } else {
                    $sql .= ' AND a.category_id = ?';
                    $args[] = $categoryId;
                }
            }
            if ($keyword !== '') { $sql .= ' AND a.name LIKE ?'; $args[] = '%' . $keyword . '%'; }
            // 排序: 置顶优先 → 带「新」标的在前 → 按发布/创建时间倒序 → id 兜底
            $sql .= ' ORDER BY a.is_top DESC, (a.new_until IS NOT NULL AND a.new_until > NOW()) DESC, COALESCE(a.release_date, DATE(a.created_at)) DESC, a.id DESC';
            $stmt = db()->prepare($sql);
            $stmt->execute($args);
            $apps = $stmt->fetchAll();
            foreach ($apps as &$app) {
                $app['download_count'] = (int)$app['download_count'];
                $app['rating'] = (float)$app['rating'];
                $app['id'] = (int)$app['id'];
                $app['pack_id'] = $app['pack_id'] ? (int)$app['pack_id'] : null;
                $app['is_top'] = (int)$app['is_top'];
                $app['is_featured'] = (int)$app['is_featured'];
                // 「新版本」= new_until 未过期 (版本变更或手动设置时 = now+3天, 到期自动消失)
                $app['is_new'] = ($app['new_until'] && strtotime($app['new_until']) > time()) ? 1 : 0;
                $app['release_date'] = $app['release_date'] ?: '';
                // 介绍图片: JSON 数组 -> 数组
                $app['screenshots'] = $app['screenshots'] ? (json_decode($app['screenshots'], true) ?: []) : [];
                // 投稿人 QQ 属敏感字段, 仅管理员可见 (App 端一律不给)
                if (!is_admin()) unset($app['contributor_qq']);
            }
            json_out($apps);

        case 'app_detail':
            $id = (int)param('id', 0);
            // 管理员可查看已下架软件详情 (后台编辑需要), 访客只看上架的
            $inactiveCond = ((int)param('include_inactive', 0) === 1 && is_admin()) ? '' : ' AND a.is_active = 1';
            $stmt = db()->prepare('SELECT a.*, c.name AS category_name FROM apps a
                                   LEFT JOIN categories c ON a.category_id = c.id
                                   WHERE a.id = ?' . $inactiveCond);
            $stmt->execute([$id]);
            $app = $stmt->fetch();
            if (!$app) json_error('软件不存在');
            $linkStmt = db()->prepare('SELECT id, pan_type, label, url, password FROM pan_links
                                       WHERE app_id = ? AND is_active = 1 ORDER BY sort_order ASC, id ASC');
            $linkStmt->execute([$id]);
            $app['pan_links'] = $linkStmt->fetchAll();
            // 整合包子项
            $packStmt = db()->prepare('SELECT id, name, icon, version, download_count FROM apps
                                       WHERE pack_id = ? AND is_active = 1 ORDER BY sort_order ASC, id ASC');
            $packStmt->execute([$id]);
            $app['pack_items'] = $packStmt->fetchAll();
            foreach ($app['pack_items'] as &$pi) { $pi['id'] = (int)$pi['id']; $pi['download_count'] = (int)$pi['download_count']; }
            // 所属整合包
            $app['pack_id'] = $app['pack_id'] ? (int)$app['pack_id'] : null;
            if ($app['pack_id']) {
                $pStmt = db()->prepare('SELECT id, name, icon FROM apps WHERE id = ?');
                $pStmt->execute([$app['pack_id']]);
                $app['parent_pack'] = $pStmt->fetch();
            } else {
                $app['parent_pack'] = null;
            }
            $app['download_count'] = (int)$app['download_count'];
            $app['rating'] = (float)$app['rating'];
            $app['id'] = (int)$app['id'];
            $app['is_top'] = (int)$app['is_top'];
            $app['is_featured'] = (int)$app['is_featured'];
            // 「新版本」= new_until 未过期 (版本变更或手动设置时 = now+3天, 到期自动消失)
            $app['is_new'] = ($app['new_until'] && strtotime($app['new_until']) > time()) ? 1 : 0;
            $app['release_date'] = $app['release_date'] ?: '';
            // 介绍图片: JSON 数组 -> 数组
            $app['screenshots'] = $app['screenshots'] ? (json_decode($app['screenshots'], true) ?: []) : [];
            // 投稿人 QQ 属敏感字段, 仅管理员可见
            if (!is_admin()) unset($app['contributor_qq']);
            json_out($app);

        case 'app_create':
            require_admin();
            $app = insert_app();
            // 新添加的软件默认不标新; 若管理端手动勾选「新版本标」→ 标新 3 天
            $nf = param('new_flag', null);
            if ($nf !== null) {
                $st = db()->prepare('UPDATE apps SET new_until = ? WHERE id = ?');
                $st->execute([(int)$nf ? date('Y-m-d H:i:s', time() + 3 * 86400) : null, $app]);
            }
            json_out(['id' => (int)$app]);

        case 'app_update':
            require_admin();
            $id = (int)param('id', 0);
            $fields = ['name', 'category_id', 'icon', 'version', 'description', 'screenshots', 'package_name', 'rating', 'sort_order', 'is_active', 'pack_id', 'is_top', 'is_featured', 'contributor_qq', 'release_date'];
            // 「新版本」完全手动控制: new_flag=1 → 标「新」3 天 (到期自动回普通); new_flag=0 → 立即取消; 不传 → 不动
            // 改版本号等任何其他操作都不会自动标新
            $newFlag = param('new_flag', null);
            $sql = 'UPDATE apps SET ';
            $args = [];
            foreach ($fields as $f) {
                $v = param($f);
                if ($v !== null) {
                    if ($f === 'pack_id') { $sql .= "pack_id = ?, "; $args[] = (int)$v ?: null; }
                    elseif ($f === 'is_top' || $f === 'is_featured') { $sql .= "$f = ?, "; $args[] = (int)$v ?: 0; }
                    elseif ($f === 'release_date') { $sql .= "release_date = ?, "; $args[] = $v === '' ? null : $v; }
                    else { $sql .= "$f = ?, "; $args[] = $v; }
                }
            }
            if ($newFlag !== null) {
                $sql .= (int)$newFlag ? 'new_until = DATE_ADD(NOW(), INTERVAL 3 DAY), ' : 'new_until = NULL, ';
            }
            if (!$args && $newFlag === null) json_error('没有要更新的字段');
            $sql = rtrim($sql, ', ') . ' WHERE id = ?';
            $args[] = $id;
            db()->prepare($sql)->execute($args);
            json_out(null);

        case 'app_delete':
            require_admin();
            $id = (int)param('id', 0);
            db()->prepare('DELETE FROM apps WHERE id = ?')->execute([$id]);
            db()->prepare('DELETE FROM pan_links WHERE app_id = ?')->execute([$id]);
            json_out(null);

        // ============ 网盘推广链接 ============
        case 'link_create':
            require_admin();
            $appId = (int)param('app_id', 0);
            $label = param('label', 'UC网盘');
            $url = param('url', '');
            $panType = param('pan_type', 'uc');
            $password = param('password', '');
            if (!$url) json_error('链接地址不能为空');
            db()->prepare('INSERT INTO pan_links (app_id, pan_type, label, url, password) VALUES (?, ?, ?, ?, ?)')
                ->execute([$appId, $panType, $label, $url, $password]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'link_update':
            require_admin();
            $id = (int)param('id', 0);
            $fields = ['pan_type', 'label', 'url', 'password', 'sort_order'];
            $sql = 'UPDATE pan_links SET ';
            $args = [];
            foreach ($fields as $f) {
                $v = param($f);
                if ($v !== null) { $sql .= "$f = ?, "; $args[] = $v; }
            }
            if (!$args) json_error('没有要更新的字段');
            $sql = rtrim($sql, ', ') . ' WHERE id = ?';
            $args[] = $id;
            db()->prepare($sql)->execute($args);
            json_out(null);

        case 'link_delete':
            require_admin();
            $id = (int)param('id', 0);
            db()->prepare('DELETE FROM pan_links WHERE id = ?')->execute([$id]);
            json_out(null);

        case 'link_click':
            $id = (int)param('id', 0);
            $stmt = db()->prepare('SELECT * FROM pan_links WHERE id = ? AND is_active = 1');
            $stmt->execute([$id]);
            $link = $stmt->fetch();
            if (!$link) json_error('链接不存在');
            // 记录点击
            db()->prepare('INSERT INTO clicks (link_id, ip, user_agent, referer) VALUES (?, ?, ?, ?)')
                ->execute([$id, client_ip(), substr($_SERVER['HTTP_USER_AGENT'] ?? '', 0, 255), substr($_SERVER['HTTP_REFERER'] ?? '', 0, 255)]);
            // 下载量 +1
            db()->prepare('UPDATE apps SET download_count = download_count + 1 WHERE id = ?')
                ->execute([$link['app_id']]);
            json_out(['url' => $link['url'], 'password' => $link['password']]);

        // ============ 图片上传 (管理) ============
        case 'upload':
            require_admin();
            @set_time_limit(300);
            if (empty($_FILES['file'])) json_error('未收到文件');
            $file = $_FILES['file'];
            $ext = strtolower(pathinfo($file['name'], PATHINFO_EXTENSION));
            // type=apk 允许 APK (版本发布), 否则仅图片
            $isApk = param('type', '') === 'apk';
            $allowed = $isApk ? ['apk'] : ['jpg', 'jpeg', 'png', 'gif', 'webp'];
            if (!in_array($ext, $allowed)) json_error('不支持的文件类型: ' . $ext);
            $maxSize = $isApk ? 200 * 1024 * 1024 : 10 * 1024 * 1024;
            if ($file['size'] > $maxSize) json_error('文件不能超过 ' . ($isApk ? '200MB' : '10MB'));
            $raw = file_get_contents($file['tmp_name']);
            if ($raw === false) json_error('读取文件失败');
            // 图片自动压缩 (压到 1600px 宽内, JPEG/WebP 质量 80) — 不占服务器磁盘
            if (!$isApk && in_array($ext, ['jpg', 'jpeg', 'png', 'webp'])) {
                $raw = compress_image($raw, $ext, 1600, 80);
            }
            // 上传到雨云 S3 (不落服务器磁盘, 不耗服务器存储)
            $key = s3_key($isApk ? 'files' : 'images', $ext);
            $mime = $isApk ? 'application/vnd.android.package-archive'
                : (in_array($ext, ['jpg','jpeg']) ? 'image/jpeg' : ($ext === 'png' ? 'image/png' : ($ext === 'webp' ? 'image/webp' : 'image/gif')));
            if (!s3_upload_bytes($raw, $key, $mime)) json_error('上传对象存储失败');
            $url = S3_PUBLIC_URL . '/' . $key;
            json_out(['url' => $url, 'filename' => $key, 'size' => strlen($raw)]);

        // ============ 轮播图 ============
        case 'banners':
            $rows = db()->query('SELECT b.*, a.name AS app_name FROM banners b
                                 LEFT JOIN apps a ON b.app_id = a.id
                                 WHERE b.is_active = 1 ORDER BY b.sort_order ASC, b.id ASC')->fetchAll();
            foreach ($rows as &$r) { $r['id'] = (int)$r['id']; $r['app_id'] = $r['app_id'] ? (int)$r['app_id'] : null; }
            json_out($rows);

        case 'banner_create':
            require_admin();
            db()->prepare('INSERT INTO banners (image, title, app_id, url, sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?)')
                ->execute([param('image', ''), param('title', ''), (int)param('app_id', 0) ?: null, param('url', ''), (int)param('sort_order', 0), (int)param('is_active', 1)]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'banner_update':
            require_admin();
            $id = (int)param('id', 0);
            $fields = ['image', 'title', 'app_id', 'url', 'sort_order', 'is_active'];
            $sql = 'UPDATE banners SET ';
            $args = [];
            foreach ($fields as $f) {
                $v = param($f);
                if ($v !== null) { $sql .= "$f = ?, "; $args[] = ($f === 'app_id') ? ((int)$v ?: null) : $v; }
            }
            if (!$args) json_error('没有要更新的字段');
            $sql = rtrim($sql, ', ') . ' WHERE id = ?';
            $args[] = $id;
            db()->prepare($sql)->execute($args);
            json_out(null);

        case 'banner_delete':
            require_admin();
            $id = (int)param('id', 0);
            db()->prepare('DELETE FROM banners WHERE id = ?')->execute([$id]);
            json_out(null);

        // ============ 统计 (管理) ============
        case 'stats':
            require_admin();
            $totalApps = (int)db()->query('SELECT COUNT(*) FROM apps')->fetchColumn();
            $totalDownloads = (int)db()->query('SELECT COALESCE(SUM(download_count),0) FROM apps')->fetchColumn();
            $totalClicks = (int)db()->query('SELECT COUNT(*) FROM clicks')->fetchColumn();
            $todayClicks = (int)db()->query("SELECT COUNT(*) FROM clicks WHERE created_at >= CURDATE()")->fetchColumn();
            $topLinks = db()->query('SELECT l.id, l.label, COUNT(c.id) AS cnt FROM clicks c
                                     JOIN pan_links l ON c.link_id = l.id
                                     GROUP BY l.id ORDER BY cnt DESC LIMIT 10')->fetchAll();
            // 软件打开排行: 按软件聚合下载点击次数 (用户要求"所有统计")
            $topApps = db()->query('SELECT a.id, a.name, a.icon, COUNT(c.id) AS cnt
                                    FROM clicks c
                                    JOIN pan_links l ON c.link_id = l.id
                                    JOIN apps a ON l.app_id = a.id
                                    GROUP BY a.id ORDER BY cnt DESC LIMIT 10')->fetchAll();
            foreach ($topApps as &$ta) { $ta['id'] = (int)$ta['id']; $ta['cnt'] = (int)$ta['cnt']; }
            // 今日软件打开排行
            $topAppsToday = db()->query("SELECT a.id, a.name, a.icon, COUNT(c.id) AS cnt
                                    FROM clicks c
                                    JOIN pan_links l ON c.link_id = l.id
                                    JOIN apps a ON l.app_id = a.id
                                    WHERE c.created_at >= CURDATE()
                                    GROUP BY a.id ORDER BY cnt DESC LIMIT 10")->fetchAll();
            foreach ($topAppsToday as &$tat) { $tat['id'] = (int)$tat['id']; $tat['cnt'] = (int)$tat['cnt']; }
            json_out([
                'total_apps' => $totalApps,
                'total_downloads' => $totalDownloads,
                'total_clicks' => $totalClicks,
                'today_clicks' => $todayClicks,
                'top_links' => $topLinks,
                'top_apps' => $topApps,
                'top_apps_today' => $topAppsToday,
            ]);

        // ============ APK 上传 + 版本发布 (管理) ============
        case 'upload_apk':
            require_admin();
            if (empty($_FILES['file'])) json_error('未收到文件');
            $file = $_FILES['file'];
            if ($file['error'] !== UPLOAD_ERR_OK) json_error('上传错误: ' . $file['error']);
            $size = $file['size'];
            if ($size <= 0 || $size > 200 * 1024 * 1024) json_error('文件大小无效 (最大 200MB)');
            $ext = strtolower(pathinfo($file['name'], PATHINFO_EXTENSION));
            if ($ext !== 'apk') json_error('仅支持 .apk 文件');
            // 上传到雨云 S3 (大文件流式, 不占服务器磁盘)
            $key = 'apk/fengling_' . date('Ymd_His') . '.apk';
            $path = "/" . S3_BUCKET . "/" . $key;
            $payloadHash = hash('sha256', ''); // 流式用 UNSIGNED-PAYLOAD
            $ch = curl_init(S3_ENDPOINT . $path);
            $fp = fopen($file['tmp_name'], 'rb');
            if (!$fp) json_error('读取文件失败');
            // 流式上传需要 x-amz-content-sha256: UNSIGNED-PAYLOAD
            $now = gmdate('Ymd\THis\Z');
            $dateStamp = gmdate('Ymd');
            $host = parse_url(S3_ENDPOINT, PHP_URL_HOST);
            $canonicalHeaders = "host:" . $host . "\nx-amz-content-sha256:UNSIGNED-PAYLOAD\nx-amz-date:" . $now . "\n";
            $signedHeaders = "host;x-amz-content-sha256;x-amz-date";
            $canonicalRequest = "PUT\n" . $path . "\n\n" . $canonicalHeaders . "\n" . $signedHeaders . "\nUNSIGNED-PAYLOAD";
            $scope = $dateStamp . "/" . S3_REGION . "/s3/aws4_request";
            $stringToSign = "AWS4-HMAC-SHA256\n" . $now . "\n" . $scope . "\n" . hash('sha256', $canonicalRequest);
            $kDate = s3_hmac("AWS4" . S3_SECRET_KEY, $dateStamp);
            $kRegion = s3_hmac($kDate, S3_REGION);
            $kService = s3_hmac($kRegion, "s3");
            $kSigning = s3_hmac($kService, "aws4_request");
            $signature = hash_hmac('sha256', $stringToSign, $kSigning);
            $auth = "AWS4-HMAC-SHA256 Credential=" . S3_ACCESS_KEY . "/" . $scope .
                ", SignedHeaders=" . $signedHeaders . ", Signature=" . $signature;
            curl_setopt_array($ch, [
                CURLOPT_PUT => true,
                CURLOPT_INFILE => $fp,
                CURLOPT_INFILESIZE => $size,
                CURLOPT_HTTPHEADER => [
                    "Authorization: " . $auth,
                    "x-amz-date: " . $now,
                    "x-amz-content-sha256: UNSIGNED-PAYLOAD",
                    "Content-Type: application/vnd.android.package-archive",
                ],
                CURLOPT_RETURNTRANSFER => true,
                CURLOPT_CONNECTTIMEOUT => 20,
                CURLOPT_TIMEOUT => 600,
            ]);
            $resp = curl_exec($ch);
            $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
                    fclose($fp);
            if ($code < 200 || $code >= 300) json_error('上传对象存储失败 (HTTP ' . $code . ')');
            json_out(['url' => S3_PUBLIC_URL . '/' . $key, 'size' => $size]);

        case 'version_update':
            require_admin();
            $version = param('version', '');
            $url = param('url', '');
            $update_log = param('update_log', '');
            $update_mode = param('update_mode', 'internal'); // internal=内置浏览器, external=外置浏览器
            $force_update = (int)param('force_update', 0); // 1=强制更新
            // APK 大小: 未填时自动获取 (本地旧路径或 S3 URL)
            $size_mb = (float)param('size_mb', 0);
            if ($size_mb <= 0 && $url) {
                if (strpos($url, '/uploads/apk/') !== false) {
                    $filePath = __DIR__ . parse_url($url, PHP_URL_PATH);
                    if (file_exists($filePath)) {
                        $size_mb = round(filesize($filePath) / 1048576, 1);
                    }
                } elseif (strpos($url, S3_PUBLIC_URL) === 0 || strpos($url, S3_ENDPOINT) === 0) {
                    // S3 对象: HEAD 请求拿 Content-Length (不下载)
                    $ch = curl_init($url);
                    curl_setopt_array($ch, [
                        CURLOPT_NOBODY => true,
                        CURLOPT_RETURNTRANSFER => true,
                        CURLOPT_CONNECTTIMEOUT => 10,
                        CURLOPT_TIMEOUT => 30,
                    ]);
                    curl_exec($ch);
                    $len = curl_getinfo($ch, CURLINFO_CONTENT_LENGTH_DOWNLOAD);
                                    if ($len > 0) $size_mb = round($len / 1048576, 1);
                }
            }
            // 发布日期: 未填时默认当天
            $release_date = param('release_date', date('Y-m-d'));
            if (!$release_date) $release_date = date('Y-m-d');
            if (!$version) json_error('版本号不能为空');
            $cfg = json_encode([
                'version' => $version,
                'url' => $url,
                'update_log' => $update_log,
                'update_mode' => $update_mode,
                'force_update' => $force_update,
                'size_mb' => $size_mb,
                'release_date' => $release_date,
            ], JSON_UNESCAPED_UNICODE);
            db()->prepare("INSERT INTO settings (`key`, `value`) VALUES ('latest_version', ?)
                           ON DUPLICATE KEY UPDATE `value` = ?")->execute([$cfg, $cfg]);
            json_out(null);

        // ============ 关于页配置 (官方频道等) ============
        case 'about_config_get':
            // 公开: 返回关于页配置 (官方频道/链接)
            $row = db()->query("SELECT * FROM settings WHERE `key` = 'about_config'")->fetch(PDO::FETCH_ASSOC);
            $cfg = $row ? json_decode($row['value'], true) : [];
            $defaults = [
                'qq_group' => '',      // QQ 群号
                'qq_key' => '',        // qq 加群 key (mqqapi 用)
                'qq_url' => '',        // 加群网页链接
                'qq_channel' => '',    // QQ 频道链接
                'website' => '',       // 官网
                'github' => '',        // GitHub
                'feedback' => '',      // 反馈
                'donate' => '',        // 捐赠
                'banner_text' => '风铃分享库 · 官方频道',
                'banner_sub' => '最新软件 · 更新通知 · 交流反馈',
            ];
            json_out(array_merge($defaults, $cfg));

        case 'about_config_set':
            require_admin();
            $cfg = json_encode([
                'qq_group' => param('qq_group', ''),
                'qq_key' => param('qq_key', ''),
                'qq_url' => param('qq_url', ''),
                'qq_channel' => param('qq_channel', ''),
                'website' => param('website', ''),
                'github' => param('github', ''),
                'feedback' => param('feedback', ''),
                'donate' => param('donate', ''),
                'banner_text' => param('banner_text', ''),
                'banner_sub' => param('banner_sub', ''),
            ], JSON_UNESCAPED_UNICODE);
            db()->prepare("INSERT INTO settings (`key`, `value`) VALUES ('about_config', ?)
                           ON DUPLICATE KEY UPDATE `value` = ?")->execute([$cfg, $cfg]);
            json_out(null);

        case 'version':
            // 版本检测 (公开): 返回最新版本信息
            // 版本配置存 settings 表 latest_version
            $version = '1.0.0';
            $url = '';
            $update_log = '';
            $update_mode = 'internal';
            $row = db()->query("SELECT * FROM settings WHERE `key` = 'latest_version'")->fetch(PDO::FETCH_ASSOC);
            if ($row) {
                $cfg = json_decode($row['value'], true);
                $version = $cfg['version'] ?? $version;
                $url = $cfg['url'] ?? '';
                $update_log = $cfg['update_log'] ?? '';
                $update_mode = $cfg['update_mode'] ?? 'internal';
                $force_update = (int)($cfg['force_update'] ?? 0);
                $size_mb = (float)($cfg['size_mb'] ?? 0);
                $release_date = $cfg['release_date'] ?? '';
            }
            json_out([
                'version' => $version,
                'url' => $url,
                'update_log' => $update_log,
                'update_mode' => $update_mode,
                'force_update' => $force_update,
                'size_mb' => $size_mb,
                'release_date' => $release_date,
            ]);

        // ============ 投稿名单 (公开: 只返回头像/昵称/说明, 绝不暴露 QQ 号) ============
        case 'contributors':
            // 投稿人来自 contributors 表, 投稿应用从 apps.contributor_qq 聚合
            $rows = db()->query("SELECT c.id, c.qq, c.name, c.bio, c.sort_order, a.name AS app_name
                                 FROM contributors c
                                 LEFT JOIN apps a ON a.contributor_qq = c.qq AND a.is_active = 1
                                 ORDER BY c.sort_order ASC, c.id ASC")->fetchAll();
            $map = [];
            $order = [];
            foreach ($rows as $r) {
                $qq = trim($r['qq']);
                if ($qq === '') continue;
                if (!isset($map[$qq])) {
                    $order[] = $qq;
                    $map[$qq] = [
                        'id' => (int)$r['id'],
                        // q1.qlogo.cn 是 QQ 官方头像 CDN: /g?b=qq&nk={qq}&s=100 按 QQ 号取头像
                        'avatar' => 'https://q1.qlogo.cn/g?b=qq&nk=' . urlencode($qq) . '&s=100',
                        'name' => $r['name'] !== null ? $r['name'] : '',
                        'bio' => $r['bio'] !== null ? $r['bio'] : '',
                        'app_names' => [],
                    ];
                }
                if ($r['app_name'] !== null) $map[$qq]['app_names'][] = $r['app_name'];
            }
            $result = [];
            foreach ($order as $qq) $result[] = $map[$qq];
            json_out($result);

        case 'contributor_create':
            require_admin();
            $qq = trim(param('qq', ''));
            if ($qq === '' || !preg_match('/^\d{5,12}$/', $qq)) json_error('QQ 号格式不正确');
            $name = trim(param('name', ''));
            $bio = trim(param('bio', ''));
            $sort = (int)param('sort_order', 0);
            $st = db()->prepare('SELECT COUNT(*) FROM contributors WHERE qq = ?');
            $st->execute([$qq]);
            if ((int)$st->fetchColumn() > 0) json_error('该 QQ 已在投稿名单中');
            db()->prepare('INSERT INTO contributors (qq, name, bio, sort_order) VALUES (?, ?, ?, ?)')->execute([$qq, $name, $bio, $sort]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'contributor_update':
            require_admin();
            $id = (int)param('id', 0);
            $fields = ['name', 'bio', 'sort_order'];
            $sql = 'UPDATE contributors SET ';
            $args = [];
            foreach ($fields as $f) {
                $v = param($f);
                if ($v !== null) { $sql .= "$f = ?, "; $args[] = trim((string)$v); }
            }
            if (!$args) json_error('没有要更新的字段');
            $sql = rtrim($sql, ', ') . ' WHERE id = ?';
            $args[] = $id;
            db()->prepare($sql)->execute($args);
            json_out(null);

        case 'contributor_delete':
            require_admin();
            $id = (int)param('id', 0);
            db()->prepare('DELETE FROM contributors WHERE id = ?')->execute([$id]);
            json_out(null);

        case 'contributor_apps':
            // 管理端: 返回投稿人 + 其投稿的应用 (管理专用, 含 QQ)
            require_admin();
            $rows = db()->query("SELECT c.id, c.qq, c.name, c.bio, c.sort_order, a.id AS app_id, a.name AS app_name
                                 FROM contributors c
                                 LEFT JOIN apps a ON a.contributor_qq = c.qq AND a.is_active = 1
                                 ORDER BY c.sort_order ASC, c.id ASC")->fetchAll();
            $map = [];
            $order = [];
            foreach ($rows as $r) {
                $qq = trim($r['qq']);
                if ($qq === '') continue;
                if (!isset($map[$qq])) {
                    $order[] = $qq;
                    $map[$qq] = [
                        'id' => (int)$r['id'],
                        'qq' => $qq,
                        'name' => $r['name'] !== null ? $r['name'] : '',
                        'bio' => $r['bio'] !== null ? $r['bio'] : '',
                        'app_names' => [],
                    ];
                }
                if ($r['app_name'] !== null) $map[$qq]['app_names'][] = $r['app_name'];
            }
            $result = [];
            foreach ($order as $qq) $result[] = $map[$qq];
            json_out($result);

        // ============ 公告 ============
        case 'notice_get':
            // 公告 (公开): content + mode (daily=每日一次, every=每次打开) + enabled
            $row = db()->query("SELECT * FROM settings WHERE `key` = 'notice'")->fetch(PDO::FETCH_ASSOC);
            $cfg = $row ? json_decode($row['value'], true) : [];
            $defaults = [
                'content' => '',
                'mode' => 'daily',   // daily=每日显示一次, every=每次打开显示
                'enabled' => 0,      // 1=启用公告
            ];
            json_out(array_merge($defaults, $cfg));

        case 'notice_set':
            require_admin();
            $cfg = json_encode([
                'content' => param('content', ''),
                'mode' => param('mode', 'daily'),
                'enabled' => (int)param('enabled', 0) ? 1 : 0,
            ], JSON_UNESCAPED_UNICODE);
            db()->prepare("INSERT INTO settings (`key`, `value`) VALUES ('notice', ?)
                           ON DUPLICATE KEY UPDATE `value` = ?")->execute([$cfg, $cfg]);
            json_out(null);

        case 'crash_report':
            // 崩溃日志上报 (App 端匿名提交, 无需登录; 提交时堆栈截断防刷库)
            $stack = (string)param('stack', '');
            if (mb_strlen($stack) > 20000) $stack = mb_substr($stack, 0, 20000);
            if ($stack === '') json_error('stack 为空', 400);
            db()->prepare("INSERT INTO crash_reports (device, android_version, app_version, stack, ip) VALUES (?, ?, ?, ?, ?)")
                ->execute([
                    mb_substr((string)param('device', ''), 0, 100),
                    mb_substr((string)param('android_version', ''), 0, 30),
                    mb_substr((string)param('app_version', ''), 0, 30),
                    $stack,
                    client_ip(),
                ]);
            json_out(null);

        case 'crash_reports':
            // 崩溃日志列表 (管理端)
            require_admin();
            $rows = db()->query("SELECT * FROM crash_reports ORDER BY id DESC LIMIT 100")->fetchAll();
            json_out($rows);

        case 'harm_report':
            // 反馈和谐提交 (App 端匿名, 无需登录; content 截断防刷库)
            $content = (string)param('content', '');
            if (mb_strlen($content) > 500) $content = mb_substr($content, 0, 500);
            if (trim($content) === '') json_error('请填写哪里被和谐了', 400);
            $contact = (string)param('contact', '');
            if (mb_strlen($contact) > 100) $contact = mb_substr($contact, 0, 100);
            // 防恶意刷反馈 (2026-08-07):
            // ① 同 IP 60 秒内最多 1 条 (频率限制)
            // ② 同 IP 同一软件 10 分钟内最多 1 条 (防反复刷同一软件)
            $ip = client_ip();
            $st = db()->prepare("SELECT COUNT(*) FROM harm_reports WHERE ip = ? AND created_at > (NOW() - INTERVAL 60 SECOND)");
            $st->execute([$ip]);
            if ((int)$st->fetchColumn() > 0) json_error('提交太频繁，请稍后再试', 429);
            $st = db()->prepare("SELECT COUNT(*) FROM harm_reports WHERE ip = ? AND app_id = ? AND created_at > (NOW() - INTERVAL 600 SECOND)");
            $st->execute([$ip, (int)param('app_id', 0)]);
            if ((int)$st->fetchColumn() > 0) json_error('该软件刚刚反馈过，请勿重复提交', 429);
            db()->prepare("INSERT INTO harm_reports (app_id, app_name, content, contact, ip) VALUES (?, ?, ?, ?, ?)")
                ->execute([
                    (int)param('app_id', 0),
                    mb_substr((string)param('app_name', ''), 0, 100),
                    $content,
                    $contact,
                    $ip,
                ]);
            json_out(null);

        case 'harm_reports':
            // 反馈和谐列表 (管理端)
            require_admin();
            $rows = db()->query("SELECT * FROM harm_reports ORDER BY status ASC, id DESC LIMIT 200")->fetchAll();
            json_out($rows);

        case 'harm_report_delete':
            // 删除反馈 (管理端)
            require_admin();
            db()->prepare("DELETE FROM harm_reports WHERE id = ?")->execute([(int)param('id', 0)]);
            json_out(null);

        case 'harm_report_status':
            // 标记已处理/未处理 (管理端)
            require_admin();
            db()->prepare("UPDATE harm_reports SET status = ? WHERE id = ?")
                ->execute([(int)param('status', 0) === 1 ? 1 : 0, (int)param('id', 0)]);
            json_out(null);

        // ============ UC 网盘: 扫码登录 + 分享解析 ============
        case 'uc_status':
            require_admin();
            json_out(UcDrive::status());

        case 'uc_qr_create':
            require_admin();
            $uc = UcDrive::qrCreate();
            if ((int)$uc['code'] !== 0) json_error($uc['msg'], 502);
            json_out($uc['data']);

        case 'uc_qr_poll':
            require_admin();
            $uc = UcDrive::qrPoll(param('token', ''));
            if ((int)$uc['code'] !== 0) json_error($uc['msg'], (int)$uc['code'] === 400 ? 400 : 502);
            json_out($uc['data']);

        case 'uc_cookie_set':
            require_admin();
            $uc = UcDrive::loginByCookie(param('cookie', ''));
            if ((int)$uc['code'] !== 0) json_error($uc['msg'], 400);
            json_out($uc['data']);

        case 'uc_logout':
            require_admin();
            UcDrive::clearAuth();
            json_out(null);

        case 'uc_resolve':
            require_admin();
            $uc = UcDrive::resolve(param('url', ''), param('pwd', ''));
            if ((int)$uc['code'] !== 0) {
                json_error($uc['msg'], (int)$uc['code'] === 401 ? 401 : ((int)$uc['code'] === 400 || !empty($uc['need_pwd']) ? 400 : 502));
            }
            $d = $uc['data'];
            $rank = ['apk' => 5, 'zip' => 4, 'rar' => 4, '7z' => 4, 'xapk' => 3, 'apks' => 3];
            $main = null;
            foreach ($d['files'] as $f) {
                if (!empty($f['isdir'])) continue;
                $w = isset($rank[$f['ext']]) ? $rank[$f['ext']] : 1;
                if ($main === null || $w > $main['_w'] || ($w === $main['_w'] && $f['size'] > $main['size'])) {
                    $f['_w'] = $w;
                    $main = $f;
                }
            }
            $guess = ['name' => '', 'version' => ''];
            if ($main) $guess = UcDrive::guessNameVersion($main['name']);
            $d['suggest'] = [
                'file'     => $main ? $main['name'] : '',
                'name'     => $guess['name'],
                'version'  => $guess['version'],
                'size_mb'  => $main ? $main['size_mb'] : 0,
                'kind'     => $main ? $main['ext'] : '',
                'has_apk'  => false,
                'images'   => [],
                'packages' => [],
            ];
            foreach ($d['files'] as $f) {
                if (!empty($f['isdir'])) continue;
                if ($f['ext'] === 'apk') $d['suggest']['has_apk'] = true;
                if (in_array($f['ext'], ['jpg', 'jpeg', 'png', 'webp'])) $d['suggest']['images'][] = $f['name'];
                if (in_array($f['ext'], ['apk', 'zip', 'rar', '7z', 'xapk', 'apks'])) $d['suggest']['packages'][] = $f['name'];
            }
            json_out($d);

        case 'uc_import':
            require_admin();
            @set_time_limit(600);
            $auth = UcDrive::loadAuth();
            if (empty($auth['cookie'])) json_error('UC 账号未登录，请先扫码登录', 401);
            $want = param('kind', 'all');
            $raw  = UcDrive::resolve(param('url', ''), param('pwd', ''));
            if ((int)$raw['code'] !== 0) json_error($raw['msg'], (int)$raw['code'] === 401 ? 401 : 502);
            $key    = $raw['data']['share_key'];
            $stoken = isset($raw['data']['stoken']) ? $raw['data']['stoken'] : '';
            $cookie = $auth['cookie'];
            $files  = $raw['data']['files'];
            $result = array('images' => array(), 'apk' => null, 'errors' => array());

            if ($want === 'images' || $want === 'all') {
                foreach ($files as $f) {
                    if (!empty($f['isdir'])) continue;
                    if (!in_array($f['ext'], array('jpg', 'jpeg', 'png', 'webp', 'gif'), true)) continue;
                    if (count($result['images']) >= 15) break;
                    $tmp = UcDrive::downloadToTmp($key, $stoken, $f['fid'], $f['fid_token'], $cookie);
                    if ($tmp === '') { $result['errors'][] = '下载失败: ' . $f['name']; continue; }
                    $data = @file_get_contents($tmp);
                    @unlink($tmp);
                    if ($data === false || $data === '') { $result['errors'][] = '读取失败: ' . $f['name']; continue; }
                    $ext = $f['ext'] === 'jpeg' ? 'jpg' : $f['ext'];
                    $data = compress_image($data, $ext, 1600, 82);
                    $objKey = s3_key('screenshots', $ext);
                    $mime = ($ext === 'jpg') ? 'image/jpeg' : 'image/' . $ext;
                    if (s3_upload_bytes($data, $objKey, $mime)) {
                        $result['images'][] = S3_PUBLIC_URL . '/' . $objKey;
                    } else {
                        $result['errors'][] = '上传失败: ' . $f['name'];
                    }
                }
            }

            if ($want === 'apk' || $want === 'all') {
                $apk = null;
                foreach ($files as $f) {
                    if (!empty($f['isdir']) || $f['ext'] !== 'apk') continue;
                    if ($apk === null || $f['size'] > $apk['size']) $apk = $f;
                }
                if ($apk === null) {
                    $result['errors'][] = '分享里没有 apk 文件';
                } else {
                    $tmp = UcDrive::downloadToTmp($key, $stoken, $apk['fid'], $apk['fid_token'], $cookie);
                    if ($tmp === '') {
                        $result['errors'][] = '安装包下载失败（可能超过服务器限制）';
                    } else {
                        $p = ApkParser::parse($tmp);
                        @unlink($tmp);
                        $iconUrl = '';
                        if (!empty($p['icon_data'])) {
                            $ext = in_array($p['icon_ext'], array('png', 'webp', 'jpg'), true) ? $p['icon_ext'] : 'png';
                            // 有些包里的图标是 4096x4096 的原图 (近 1MB), 压到 512px 以内再上传
                            $iconRaw = compress_image($p['icon_data'], $ext, 512, 90);
                            $objKey = s3_key('icons', $ext);
                            $mime = ($ext === 'jpg') ? 'image/jpeg' : 'image/' . $ext;
                            if (s3_upload_bytes($iconRaw, $objKey, $mime)) {
                                $iconUrl = S3_PUBLIC_URL . '/' . $objKey;
                            } else {
                                $result['errors'][] = '图标上传失败, 请手动上传图标';
                            }
                        } else {
                            // 提取不到时明确提示, 不再静默留空 (用户 2026-10-02 反馈过「图标没填上」)
                            $result['errors'][] = '未能从安装包中提取到图标 (资源可能被混淆), 请手动上传';
                        }
                        $result['apk'] = array(
                            'file'         => $apk['name'],
                            'size_mb'      => $apk['size_mb'],
                            'package'      => $p['package'],
                            'version_name' => $p['version_name'],
                            'version_code' => $p['version_code'],
                            'icon_url'     => $iconUrl,
                        );
                        if (!empty($p['error'])) $result['errors'][] = $p['error'];
                    }
                }
            }
            json_out($result);

        default:
            json_error('未知操作: ' . $action, 404);
    }
} catch (Throwable $e) {
    json_error('服务器错误: ' . $e->getMessage() . ' @ ' . basename($e->getFile()) . ':' . $e->getLine(), 500);
}

/** 分类是否存在 */
function category_exists(int $id): bool
{
    $st = db()->prepare('SELECT 1 FROM categories WHERE id = ?');
    $st->execute([$id]);
    return (bool)$st->fetch();
}

/** 管理员校验: 从 Authorization 头或 token 参数取 token */
function require_admin(): void
{
    $auth = $_SERVER['HTTP_AUTHORIZATION'] ?? '';
    $token = '';
    if (preg_match('/Bearer\s+(\S+)/i', $auth, $m)) {
        $token = $m[1];
    } else {
        $token = param('token', '');
    }
    if (!$token) json_error('未登录', 401);
    // 多会话: 优先查 sessions 表, 兼容旧 users.token
    $stmt = db()->prepare('SELECT u.id, u.role FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = ? AND u.is_active = 1');
    $stmt->execute([$token]);
    $user = $stmt->fetch();
    if (!$user) {
        $stmt = db()->prepare('SELECT id, role FROM users WHERE token = ? AND is_active = 1');
        $stmt->execute([$token]);
        $user = $stmt->fetch();
    }
    if (!$user) json_error('登录已失效', 401);
    if (($user['role'] ?? '') !== 'admin') json_error('没有权限, 仅管理员可操作', 403);
}

/** 是否为已登录管理员 (不抛错, 用于公开接口按需附带敏感字段) */
function is_admin(): bool
{
    $auth = $_SERVER['HTTP_AUTHORIZATION'] ?? '';
    $token = '';
    if (preg_match('/Bearer\s+(\S+)/i', $auth, $mm)) {
        $token = $mm[1];
    } else {
        $token = param('token', '');
    }
    if (!$token) return false;
    // 多会话: 优先查 sessions 表, 兼容旧 users.token
    $stmt = db()->prepare('SELECT u.id, u.role FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = ? AND u.is_active = 1');
    $stmt->execute([$token]);
    $row = $stmt->fetch();
    if (!$row) {
        $stmt = db()->prepare('SELECT id, role FROM users WHERE token = ? AND is_active = 1');
        $stmt->execute([$token]);
        $row = $stmt->fetch();
    }
    return $row && ($row['role'] ?? '') === 'admin';
}

/** 插入软件, 返回新 id */
function insert_app(): int
{
    $name = param('name', '');
    if (!$name) json_error('软件名不能为空');
    $categoryId = (int)param('category_id', 0) ?: null;
    $packId = (int)param('pack_id', 0) ?: null;
    db()->prepare('INSERT INTO apps (category_id, name, icon, version, description, screenshots, contributor_qq, package_name, rating, sort_order, pack_id, is_top, is_featured, release_date)
                   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)')
        ->execute([
            $categoryId,
            $name,
            param('icon', ''),
            param('version', ''),
            param('description', ''),
            param('screenshots', ''),
            param('contributor_qq', ''),
            param('package_name', ''),
            (float)param('rating', 0),
            (int)param('sort_order', 0),
            $packId,
            (int)param('is_top', 0) ?: 0,
            (int)param('is_featured', 0) ?: 0,
            // 新添加的软件不标新: release_date 由管理端填, new_until 保持 NULL (无「新」标)
            param('release_date', '') ?: null,
        ]);
    return (int)db()->lastInsertId();
}

/** 参数是否被提交过 (用于「留空不改」类更新) */
function has_param(string $key): bool
{
    // 客户端用 JSON body 提交 (ApiClient.request), 必须一并检查, 否则 user_update 会误判为"没有需要更新的内容"
    return array_key_exists($key, request_body())
        || array_key_exists($key, $_POST)
        || array_key_exists($key, $_GET);
}

/** 当前登录用户完整行 (未登录返回 null; 兼容 sessions 表与旧 users.token) */
function current_user(): ?array
{
    $auth = $_SERVER['HTTP_AUTHORIZATION'] ?? '';
    $token = '';
    if (preg_match('/Bearer\s+(\S+)/i', $auth, $m)) {
        $token = $m[1];
    } else {
        $token = param('token', '');
    }
    if (!$token) return null;
    $stmt = db()->prepare('SELECT u.* FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = ? AND u.is_active = 1');
    $stmt->execute([$token]);
    $u = $stmt->fetch();
    if ($u) return $u;
    $stmt = db()->prepare('SELECT * FROM users WHERE token = ? AND is_active = 1');
    $stmt->execute([$token]);
    $u = $stmt->fetch();
    return $u ?: null;
}

/** 当前登录用户, 未登录直接 401 */
function current_user_or_401(): array
{
    $u = current_user();
    if (!$u) json_error('登录已失效', 401);
    return $u;
}

/** 对外输出的用户字段 (剔除密码 / token / 敏感列) */
function user_public(array $u): array
{
    return [
        'id'             => (int)$u['id'],
        'username'       => $u['username'],
        'nickname'       => $u['nickname'] ?? '',
        'email'          => $u['email'] ?? '',
        'email_verified' => (int)($u['email_verified'] ?? 0),
        'avatar'         => $u['avatar'] ?? '',
        'bio'            => $u['bio'] ?? '',
        'role'           => $u['role'] ?? 'user',
        'is_active'      => (int)($u['is_active'] ?? 1),
        'created_at'     => $u['created_at'] ?? '',
    ];
}


// ==================== 社交系统辅助函数 ====================

/** 生成图形验证码, 返回 [token, code] */
function captcha_generate(): array
{
    $chars = 'ABCDEFGHJKLMNPQRSTUVWXY3456789';
    $code = '';
    $len = strlen($chars);
    for ($i = 0; $i < 4; $i++) {
        $code .= $chars[random_int(0, $len - 1)];
    }
    $token = bin2hex(random_bytes(16));
    try {
        db()->prepare('INSERT INTO captcha_codes (token, code, ip, used, expires_at) VALUES (?, ?, ?, 0, DATE_ADD(NOW(), INTERVAL 180 SECOND))')
            ->execute([$token, $code, client_ip()]);
        db()->exec('DELETE FROM captcha_codes WHERE expires_at < DATE_SUB(NOW(), INTERVAL 1 HOUR)');
    } catch (Exception $e) {
        error_log('[captcha] ' . $e->getMessage());
    }
    return [$token, $code];
}

/** 画一张 4 位验证码 PNG, 返回 data:image/png;base64,... */
function captcha_image(string $code): string
{
    // 2026-10-04 用户反馈「验证码太小看不清」: 由 132x46 放大到 280x100,
    // 并用 TrueType 字体 (34px) 绘制字符, 手机上可清晰辨认。
    $w = 280;
    $h = 100;
    $im = imagecreatetruecolor($w, $h);
    $bg = imagecolorallocate($im, 246, 247, 251);
    imagefilledrectangle($im, 0, 0, $w, $h, $bg);
    // 干扰曲线 (浅色, 不遮挡字符)
    for ($i = 0; $i < 5; $i++) {
        $c = imagecolorallocate($im, random_int(200, 232), random_int(200, 232), random_int(215, 245));
        imagearc($im, random_int(0, $w), random_int(0, $h), random_int(80, 240), random_int(50, 130), 0, 360, $c);
    }
    for ($i = 0; $i < 140; $i++) {
        $c = imagecolorallocate($im, random_int(170, 235), random_int(170, 235), random_int(170, 235));
        imagesetpixel($im, random_int(0, $w - 1), random_int(0, $h - 1), $c);
    }
    $len = max(1, strlen($code));
    $step = (int)(($w - 56) / $len);
    for ($i = 0; $i < $len; $i++) {
        // 深色字符, 与浅色背景/干扰形成强对比
        $c = imagecolorallocate($im, random_int(15, 80), random_int(15, 80), random_int(105, 185));
        $x = 26 + $i * $step;
        $y = random_int(62, 76);
        if (function_exists('imagettftext') && is_file(CAPTCHA_FONT)) {
            imagettftext($im, 34, random_int(-12, 12), $x, $y, $c, CAPTCHA_FONT, $code[$i]);
        } else {
            // 兜底: 无 TTF 时用内置字体并整体放大两倍
            imagestring($im, 5, (int)($x / 2), (int)($y / 2), $code[$i], $c);
        }
    }
    if (!function_exists('imagettftext') || !is_file(CAPTCHA_FONT)) {
        $big = imagescale($im, $w, $h);
        if ($big !== false) { imagedestroy($im); $im = $big; }
    }
    ob_start();
    imagepng($im);
    $data = ob_get_clean();
    imagedestroy($im);
    return 'data:image/png;base64,' . base64_encode($data);
}

/** 校验图形验证码 (一次性, 无论对错都作废) */
function captcha_check(string $token, string $code): bool
{
    if ($token === '' || $code === '') return false;
    try {
        $st = db()->prepare('SELECT * FROM captcha_codes WHERE token = ? LIMIT 1');
        $st->execute([$token]);
        $row = $st->fetch();
        if (!$row) return false;
        if ((int)$row['used'] === 1) return false;
        if (strtotime((string)$row['expires_at']) < time()) return false;
        db()->prepare('UPDATE captcha_codes SET used = 1 WHERE id = ?')->execute([$row['id']]);
        return strtoupper(trim($code)) === strtoupper((string)$row['code']);
    } catch (Exception $e) {
        error_log('[captcha_check] ' . $e->getMessage());
        return false;
    }
}

/** 群组字段转公开结构 (需带 member_count/message_count/notice) */
function social_group_public(array $g): array
{
    return [
        'id'            => (int)$g['id'],
        'name'          => (string)$g['name'],
        'icon'          => (string)($g['icon'] ?? ''),
        'description'   => (string)($g['description'] ?? ''),
        'notice'        => (string)($g['notice'] ?? ''),
        'member_count'  => (int)($g['member_count'] ?? 0),
        'message_count' => (int)($g['message_count'] ?? 0),
        'sort_order'    => (int)($g['sort_order'] ?? 0),
        'is_active'     => (int)($g['is_active'] ?? 1),
        'created_at'    => (string)($g['created_at'] ?? ''),
    ];
}

/** 取群组, 不存在/停用则报错 */
function social_group_or_404(int $id, bool $allowInactive)
{
    $st = db()->prepare('SELECT * FROM social_groups WHERE id = ?');
    $st->execute([$id]);
    $g = $st->fetch();
    if (!$g || (!$allowInactive && (int)$g['is_active'] !== 1)) json_error('群组不存在或已停用');
    return $g;
}

/** 消息字段转公开结构 */
function social_msg_public(array $m): array
{
    $recalled = (int)($m['is_recalled'] ?? 0) === 1;
    $at = ((string)($m['at_users'] ?? '')) !== '' ? array_map('intval', explode(',', (string)$m['at_users'])) : [];
    $ts = strtotime((string)($m['created_at'] ?? ''));
    $nick = (string)($m['nickname'] ?? '');
    if ($nick === '') $nick = '用户' . (int)$m['user_id'];
    return [
        'id'          => (int)$m['id'],
        'group_id'    => (int)$m['group_id'],
        'group_name'  => (string)($m['group_name'] ?? ''),
        'user_id'     => (int)$m['user_id'],
        'nickname'    => $nick,
        'avatar'      => (string)($m['avatar'] ?? ''),
        'role'        => (string)($m['role'] ?? 'user'),
        'content'     => $recalled ? '' : (string)$m['content'],
        'at'          => $at,
        'is_recalled' => $recalled ? 1 : 0,
        'created_at'  => (string)$m['created_at'],
        'time_text'   => date('H:i', $ts ?: time()),
    ];
}

/** 通知字段转公开结构 */
function notify_public(array $n): array
{
    return [
        'id'         => (int)$n['id'],
        'title'      => (string)$n['title'],
        'content'    => (string)$n['content'],
        'type'       => (string)$n['type'],
        'link'       => (string)($n['link'] ?? ''),
        'is_read'    => (int)$n['is_read'] === 1 ? 1 : 0,
        'created_at' => (string)$n['created_at'],
    ];
}

/** 给单个用户推送一条通知 */
function notify_push(int $userId, string $title, string $content, string $type = 'system', string $link = ''): void
{
    if ($userId <= 0) return;
    try {
        db()->prepare('INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)')
            ->execute([$userId, $title, $content, $type, $link]);
    } catch (Exception $e) {
        error_log('[notify_push] ' . $e->getMessage());
    }
}

/** 给全部启用用户推送通知, 返回成功条数 */
function notify_push_all(string $title, string $content, string $type = 'admin', string $link = ''): int
{
    $ids = db()->query('SELECT id FROM users WHERE is_active = 1')->fetchAll(PDO::FETCH_COLUMN);
    if (!$ids) return 0;
    $st = db()->prepare('INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)');
    $n = 0;
    foreach ($ids as $id) {
        try { $st->execute([(int)$id, $title, $content, $type, $link]); $n++; } catch (Exception $e) {}
    }
    return $n;
}
