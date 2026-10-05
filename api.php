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
// 时段/重置/冷却等时间计算一律以服务器本地时区(中国)为基准
date_default_timezone_set('Asia/Shanghai');

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
            // QQ 邮箱注册: 自动把 QQ 头像作为默认头像 (用户之后可在「我的 - 账号」里改)
            $qqAvatar = qq_avatar_from_email($email);
            db()->prepare("INSERT INTO users (username, password, nickname, email, email_verified, avatar, role, last_login_at) VALUES (?, ?, ?, ?, 1, ?, 'user', NOW())")
                ->execute([$username, password_hash($password, PASSWORD_DEFAULT), $nickname, $emailVal, $qqAvatar]);
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
                $where[] = '(username LIKE ? OR nickname LIKE ? OR email LIKE ? OR tags LIKE ?)';
                $like = '%' . $kw . '%';
                array_push($args, $like, $like, $like, $like);
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
                'list'      => array_map(function ($u) {
                    $p = user_public($u);
                    $p['lottery_quota'] = (int)($u['lottery_quota'] ?? -1);
                    $p['lottery_drawn'] = lottery_drawn_count((int)$u['id']);
                    $p['lottery_left']  = lottery_left_for($u);
                    return $p;
                }, $st->fetchAll()),
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
                // 邮箱改成 QQ 邮箱且该用户还没有头像: 自动补一个 QQ 头像
                $avatarNow = trim((string)($old['avatar'] ?? ''));
                if ($avatarNow === '') {
                    $qqAvatar = qq_avatar_from_email($email);
                    if ($qqAvatar !== '') {
                        $fields[] = 'avatar = ?';
                        $argsInsert = $qqAvatar;
                    }
                }
                $args[] = $id;
                if (isset($argsInsert)) {
                    // avatar 要排在 id 之前
                    array_splice($args, count($args) - 1, 0, [$argsInsert]);
                }
                db()->prepare('UPDATE users SET ' . implode(', ', $fields) . ' WHERE id = ?')->execute($args);
                // 由封禁改为启用时推一条通知
                if ($active === 1 && (int)$old['is_active'] === 0) {
                    notify_push($id, '账号已恢复', "你的账号已由管理员解除封禁, 现在可以正常登录使用了。", 'admin');
                }
            } else {
                if (strlen($password) < 6) json_error('密码至少 6 位');
                $emailVal = $email === '' ? null : $email;
                // 后台新建用户时填了 QQ 邮箱: 自动给一个 QQ 头像
                $qqAvatarNew = qq_avatar_from_email($email);
                db()->prepare('INSERT INTO users (username, password, nickname, email, email_verified, avatar, role, is_active, bio) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)')
                    ->execute([$username, password_hash($password, PASSWORD_DEFAULT), $nickname, $emailVal, $qqAvatarNew, $role, $active, $bio]);
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
            // 验证的是 QQ 邮箱且用户还没设置过头像: 自动补上 QQ 头像
            $qqAvatar = qq_avatar_from_email($email);
            if ($qqAvatar !== '') {
                db()->prepare("UPDATE users SET avatar = ? WHERE id = ? AND (avatar IS NULL OR avatar = '')")
                    ->execute([$qqAvatar, $me['id']]);
            }
            notify_push((int)$me['id'], '邮箱验证成功', '你的邮箱 ' . $email . ' 已验证成功, 可用于找回密码。', 'system');
            $st = db()->prepare('SELECT * FROM users WHERE id = ?');
            $st->execute([$me['id']]);
            json_out(['user' => user_public($st->fetch())]);

        // ============ 社交群组 ============
        // ============ 用户主页 ============
        case 'user_profile':
            $me = current_user();
            $uid = (int)param('user_id', 0);
            $gidQ = (int)param('group_id', 0);
            if ($uid <= 0) json_error('参数错误');
            $st = db()->prepare('SELECT id, username, nickname, avatar, bio, role, created_at, tags FROM users WHERE id = ? AND is_active = 1');
            $st->execute([$uid]);
            $u = $st->fetch();
            if (!$u) json_error('用户不存在');
            $st = db()->prepare('SELECT COUNT(*) FROM social_messages WHERE user_id = ? AND is_recalled = 0');
            $st->execute([(int)$u['id']]);
            $msgCount = (int)$st->fetchColumn();
            $sameGroups = 0;
            $convId = 0;
            $isMe = 0;
            if ($me) {
                $isMe = ((int)$me['id'] === (int)$u['id']) ? 1 : 0;
                $st = db()->prepare('SELECT COUNT(DISTINCT m1.group_id) FROM social_messages m1
                    INNER JOIN social_messages m2 ON m1.group_id = m2.group_id
                    WHERE m1.user_id = ? AND m2.user_id = ?');
                $st->execute([(int)$u['id'], (int)$me['id']]);
                $sameGroups = (int)$st->fetchColumn();
                if (!$isMe) $convId = pm_conv_id((int)$me['id'], (int)$u['id'], false);
            }
            // 禁言状态: 传了 group_id 就看本群, 否则看全站 (group_id = 0)
            $muteState = user_mute_state((int)$u['id'], $gidQ);
            $globalMute = $gidQ === 0 ? $muteState : user_mute_state((int)$u['id'], 0);
            $isAdminMe = ($me && (($me['role'] ?? '') === 'admin')) ? 1 : 0;
            $canMute = ($isAdminMe && !$isMe && (($u['role'] ?? '') !== 'admin')) ? 1 : 0;
            json_out([
                'id'            => (int)$u['id'],
                'username'      => (string)$u['username'],
                'nickname'      => (string)(((string)($u['nickname'] ?? '')) !== '' ? $u['nickname'] : $u['username']),
                'avatar'        => (string)($u['avatar'] ?? ''),
                'bio'           => (string)($u['bio'] ?? ''),
                'role'          => (string)($u['role'] ?? 'user'),
                'created_at'    => (string)($u['created_at'] ?? ''),
                'message_count' => $msgCount,
                'same_groups'   => $sameGroups,
                'is_me'         => $isMe,
                'can_chat'      => ($me && !$isMe) ? 1 : 0,
                'conv_id'       => $convId,
                'tags'          => user_tags_arr($u),
                'group_id'      => $gidQ,
                'muted'         => $muteState ? 1 : 0,
                'mute_left'     => $muteState ? mute_left_text($muteState) : '',
                'mute_reason'   => $muteState ? (string)($muteState['reason'] ?? '') : '',
                'global_muted'  => $globalMute ? 1 : 0,
                'global_mute_left' => $globalMute ? mute_left_text($globalMute) : '',
                'global_mute_reason' => $globalMute ? (string)($globalMute['reason'] ?? '') : '',
                'is_admin_me'   => $isAdminMe,
                'can_mute'      => $canMute,
            ]);

        // ============ 私聊 ============
        case 'pm_conversations':
            $me = current_user_or_401();
            $meId = (int)$me['id'];
            $unreadMap = pm_unread_map();
            $st = db()->prepare('SELECT c.*,
                    ua.id AS a_id, ua.username AS a_username, ua.nickname AS a_nickname, ua.avatar AS a_avatar, ua.tags AS a_tags,
                    ub.id AS b_id, ub.username AS b_username, ub.nickname AS b_nickname, ub.avatar AS b_avatar, ub.tags AS b_tags
                FROM social_pms c
                INNER JOIN users ua ON ua.id = c.user_a
                INNER JOIN users ub ON ub.id = c.user_b
                WHERE c.user_a = ? OR c.user_b = ?
                ORDER BY c.last_message_id DESC, c.id DESC LIMIT 200');
            $st->execute([$meId, $meId]);
            $list = [];
            foreach ($st->fetchAll() as $c) {
                if ((int)$c['user_a'] === $meId) {
                    $other = ['id' => $c['b_id'], 'username' => $c['b_username'], 'nickname' => $c['b_nickname'], 'avatar' => $c['b_avatar'], 'tags' => $c['b_tags']];
                } else {
                    $other = ['id' => $c['a_id'], 'username' => $c['a_username'], 'nickname' => $c['a_nickname'], 'avatar' => $c['a_avatar'], 'tags' => $c['a_tags']];
                }
                $cid = (int)$c['id'];
                $lastMsg = null;
                if ((int)$c['last_message_id'] > 0) {
                    $sm = db()->prepare('SELECT * FROM social_pm_messages WHERE id = ?');
                    $sm->execute([(int)$c['last_message_id']]);
                    $row = $sm->fetch();
                    if ($row) $lastMsg = pm_msg_public($row, $meId);
                }
                $list[] = [
                    'conv_id'   => $cid,
                    'user'      => user_brief($other),
                    'last'      => $lastMsg,
                    'last_time' => (string)($c['last_at'] ?? ''),
                    'unread'    => (int)($unreadMap[$cid]['count'] ?? 0),
                    'first_unread_id' => (int)($unreadMap[$cid]['first_id'] ?? 0),
                ];
            }
            $totalUnread = 0;
            foreach ($unreadMap as $v) { $totalUnread += (int)$v['count']; }
            json_out(['list' => $list, 'total_unread' => $totalUnread]);

        case 'pm_messages':
            $me = current_user_or_401();
            $meId = (int)$me['id'];
            $cid = (int)param('conv_id', 0);
            $otherId = (int)param('user_id', 0);
            if ($cid <= 0 && $otherId > 0) $cid = pm_conv_id($meId, $otherId, false);
            if ($cid <= 0) {
                json_out(['conv_id' => 0, 'other' => null, 'list' => [], 'has_more_before' => 0,
                    'unread' => 0, 'first_unread_id' => 0, 'my_id' => $meId]);
            }
            $st = db()->prepare('SELECT * FROM social_pms WHERE id = ?');
            $st->execute([$cid]);
            $conv = $st->fetch();
            if (!$conv || ((int)$conv['user_a'] !== $meId && (int)$conv['user_b'] !== $meId)) json_error('会话不存在');
            $otherId = pm_other_id($conv, $meId);
            $st = db()->prepare('SELECT id, username, nickname, avatar, bio, role, created_at, tags FROM users WHERE id = ?');
            $st->execute([$otherId]);
            $o = $st->fetch() ?: [];
            $limit = min(50, max(1, (int)param('limit', 30)));
            $before = (int)param('before_id', 0);
            $after = (int)param('after_id', 0);
            $around = (int)param('around_id', 0);
            if ($around > 0) {
                $st = db()->prepare('SELECT * FROM social_pm_messages WHERE conv_id = ? AND id <= ? ORDER BY id DESC LIMIT ' . $limit);
                $st->execute([$cid, $around]);
                $rows = array_reverse($st->fetchAll());
            } elseif ($before > 0) {
                $st = db()->prepare('SELECT * FROM social_pm_messages WHERE conv_id = ? AND id < ? ORDER BY id DESC LIMIT ' . $limit);
                $st->execute([$cid, $before]);
                $rows = array_reverse($st->fetchAll());
            } elseif ($after > 0) {
                $st = db()->prepare('SELECT * FROM social_pm_messages WHERE conv_id = ? AND id > ? ORDER BY id ASC LIMIT ' . $limit);
                $st->execute([$cid, $after]);
                $rows = $st->fetchAll();
            } else {
                $st = db()->prepare('SELECT * FROM social_pm_messages WHERE conv_id = ? ORDER BY id DESC LIMIT ' . $limit);
                $st->execute([$cid]);
                $rows = array_reverse($st->fetchAll());
            }
            $firstId = $rows ? (int)$rows[0]['id'] : 0;
            $hasMore = 0;
            if ($firstId > 0) {
                $st = db()->prepare('SELECT COUNT(*) FROM social_pm_messages WHERE conv_id = ? AND id < ?');
                $st->execute([$cid, $firstId]);
                $hasMore = ((int)$st->fetchColumn()) > 0 ? 1 : 0;
            }
            $unreadMap = pm_unread_map();
            json_out([
                'conv_id'  => $cid,
                'other'    => [
                    'id'         => (int)($o['id'] ?? $otherId),
                    'username'   => (string)($o['username'] ?? ''),
                    'nickname'   => (string)(((string)($o['nickname'] ?? '')) !== '' ? $o['nickname'] : ($o['username'] ?? '')),
                    'avatar'     => (string)($o['avatar'] ?? ''),
                    'bio'        => (string)($o['bio'] ?? ''),
                    'role'       => (string)($o['role'] ?? 'user'),
                    'created_at' => (string)($o['created_at'] ?? ''),
                    'tags'       => user_tags_arr($o),
                ],
                'list'     => array_map(function ($m) use ($meId) { return pm_msg_public($m, $meId); }, $rows),
                'has_more_before' => $hasMore,
                'unread'   => (int)($unreadMap[$cid]['count'] ?? 0),
                'first_unread_id' => (int)($unreadMap[$cid]['first_id'] ?? 0),
                'my_id'    => $meId,
            ]);

        case 'pm_send':
            $me = current_user_or_401();
            if ((int)$me['is_active'] !== 1) json_error('你已被封禁', 403);
            $meId = (int)$me['id'];
            $toId = (int)param('to_user', 0);
            $cid = (int)param('conv_id', 0);
            if ($toId <= 0 && $cid > 0) {
                $st = db()->prepare('SELECT * FROM social_pms WHERE id = ?');
                $st->execute([$cid]);
                $conv = $st->fetch();
                if (!$conv || ((int)$conv['user_a'] !== $meId && (int)$conv['user_b'] !== $meId)) json_error('会话不存在');
                $toId = pm_other_id($conv, $meId);
            }
            if ($toId <= 0) json_error('参数错误');
            if ($toId === $meId) json_error('不能给自己发私聊');
            // 全站禁言对私聊同样生效 (管理员不受限)
            if (($me['role'] ?? '') !== 'admin') {
                $muteState = user_mute_state($meId, 0);
                if ($muteState) {
                    $leftTxt = mute_left_text($muteState);
                    $why = trim((string)($muteState['reason'] ?? ''));
                    json_error('你已被禁言' . ($leftTxt !== '' ? ' (' . $leftTxt . ')' : '')
                        . ($why !== '' ? ', 原因: ' . $why : ''), 403);
                }
            }
            $st = db()->prepare('SELECT id, username, nickname, avatar FROM users WHERE id = ? AND is_active = 1');
            $st->execute([$toId]);
            $target = $st->fetch();
            if (!$target) json_error('对方不存在或已被封禁');
            $content = trim((string)param('content', ''));
            $image = trim((string)param('image', ''));
            if ($image !== '' && strpos($image, S3_PUBLIC_URL . '/chat/') !== 0) json_error('图片地址不合法');
            $imgW = max(0, (int)param('image_w', 0));
            $imgH = max(0, (int)param('image_h', 0));
            // 视频消息 (契约 Wave 2): 地址前缀强校验与图片一致, 尺寸/时长/大小只做范围兜底
            $video = trim((string)param('video', ''));
            if ($video !== '' && strpos($video, S3_PUBLIC_URL . '/chat/') !== 0) json_error('视频地址不合法');
            // 视频消息功能关闭时不能再发出视频消息 (契约 Wave 2: 未开启一律中文报错)
            if ($video !== '' && (int)video_config()['enabled'] !== 1) json_error('视频消息功能未开启');
            list($vidW, $vidH, $vidDur, $vidSize) = video_meta_params();
            if ($video !== '' && !db_has_column('social_pm_messages', 'video')) {
                json_error('服务端未完成视频迁移, 请联系管理员');
            }
            $msgType = $video !== '' ? 'video' : '';
            if ($content === '' && $image === '' && $video === '') json_error('消息内容不能为空');
            if (mb_strlen($content) > 500) json_error('消息不能超过 500 个字');
            $cid = pm_conv_id($meId, $toId, true);
            if ($cid <= 0) json_error('会话创建失败, 请稍后重试');
            $st = db()->prepare('SELECT created_at FROM social_pm_messages WHERE from_user = ? ORDER BY id DESC LIMIT 1');
            $st->execute([$meId]);
            $lastAt = $st->fetchColumn();
            if ($lastAt && strtotime($lastAt) > time() - 2) json_error('发送太快了, 请稍后再试');
            // 列先探测再拼 INSERT: 迁移没跑时自动退回旧的列组合 (图片消息行为一字不改)
            $pcols = ['conv_id', 'from_user', 'to_user', 'content', 'image', 'image_w', 'image_h'];
            $pvals = [$cid, $meId, $toId, $content, $image, $imgW, $imgH];
            if (db_has_column('social_pm_messages', 'video')) {
                array_push($pcols, 'video', 'video_w', 'video_h', 'video_duration', 'video_size');
                array_push($pvals, $video, $vidW, $vidH, $vidDur, $vidSize);
            }
            if (db_has_column('social_pm_messages', 'msg_type')) { $pcols[] = 'msg_type'; $pvals[] = $msgType; }
            $pcols[] = 'is_recalled';
            $pvals[] = 0;
            db()->prepare('INSERT INTO social_pm_messages (' . implode(', ', $pcols) . ') VALUES ('
                . implode(', ', array_fill(0, count($pcols), '?')) . ')')->execute($pvals);
            $mid = (int)db()->lastInsertId();
            db()->prepare('UPDATE social_pms SET last_message_id = ?, last_at = NOW() WHERE id = ?')->execute([$mid, $cid]);
            $myName = (string)(((string)($me['nickname'] ?? '')) !== '' ? $me['nickname'] : ($me['username'] ?? ''));
            $brief = $content !== '' ? mb_substr($content, 0, 60) : ($video !== '' ? '[视频]' : '[图片]');
            notify_merge($toId, $myName . ' 给你发来私聊', $brief, 'pm', 'pm:' . $cid . ':' . $mid);
            json_out(['id' => $mid, 'conv_id' => $cid, 'created_at' => date('Y-m-d H:i:s')]);

        case 'pm_read':
            $me = current_user_or_401();
            $meId = (int)$me['id'];
            $cid = (int)param('conv_id', 0);
            if ($cid <= 0) json_error('参数错误');
            $st = db()->prepare('SELECT * FROM social_pms WHERE id = ?');
            $st->execute([$cid]);
            $conv = $st->fetch();
            if (!$conv || ((int)$conv['user_a'] !== $meId && (int)$conv['user_b'] !== $meId)) json_error('会话不存在');
            $lastId = (int)param('last_id', 0);
            if ($lastId <= 0) {
                $st = db()->prepare('SELECT COALESCE(MAX(id), 0) FROM social_pm_messages WHERE conv_id = ?');
                $st->execute([$cid]);
                $lastId = (int)$st->fetchColumn();
            }
            db()->prepare('INSERT INTO social_pm_reads (user_id, conv_id, last_read_id, updated_at) VALUES (?, ?, ?, NOW())
                ON DUPLICATE KEY UPDATE last_read_id = GREATEST(last_read_id, VALUES(last_read_id)), updated_at = NOW()')
                ->execute([$meId, $cid, $lastId]);
            $st = db()->prepare('SELECT last_read_id FROM social_pm_reads WHERE user_id = ? AND conv_id = ?');
            $st->execute([$meId, $cid]);
            json_out(['ok' => true, 'conv_id' => $cid, 'last_read_id' => (int)$st->fetchColumn()]);

        case 'pm_recall':
            $me = current_user_or_401();
            $id = (int)param('id', 0);
            $st = db()->prepare('SELECT * FROM social_pm_messages WHERE id = ?');
            $st->execute([$id]);
            $m = $st->fetch();
            if (!$m) json_error('消息不存在');
            if ((int)$m['is_recalled'] === 1) json_error('消息已经撤回了');
            $isAdmin = ($me['role'] ?? '') === 'admin';
            $mine = (int)$m['from_user'] === (int)$me['id'];
            if (!$isAdmin && !$mine) json_error('没有权限撤回这条消息', 403);
            if (!$isAdmin && strtotime((string)$m['created_at']) < time() - 300) json_error('只能撤回 5 分钟内的消息');
            db()->prepare('UPDATE social_pm_messages SET is_recalled = 1, recalled_by = ?, recalled_at = NOW() WHERE id = ?')
                ->execute([(int)$me['id'], $id]);
            json_out(['ok' => true]);

        case 'social_groups':
            $rows = db()->query("SELECT g.*,
                    (SELECT COUNT(DISTINCT m.user_id) FROM social_messages m WHERE m.group_id = g.id) AS member_count,
                    (SELECT COUNT(*) FROM social_messages m WHERE m.group_id = g.id AND m.is_recalled = 0) AS message_count
                FROM social_groups g WHERE g.is_active = 1 ORDER BY g.sort_order ASC, g.id ASC")->fetchAll();
            $muted = my_muted_ids();
            $unread = my_unread_map();
            $mentions = my_mention_map();
            $lasts = group_last_messages(array_map(function ($g) { return (int)$g['id']; }, $rows));
            json_out(['list' => array_map(function ($g) use ($muted, $unread, $mentions, $lasts) {
                $gid = (int)$g['id'];
                return social_group_public(
                    $g,
                    isset($muted[$gid]) ? 1 : 0,
                    (int)($unread[$gid]['count'] ?? 0),
                    (int)($unread[$gid]['first_id'] ?? 0),
                    $lasts[$gid] ?? null,
                    $mentions[$gid] ?? []
                );
            }, $rows)]);

        case 'social_group':
            $gid = (int)param('id', 0);
            $g = social_group_or_404($gid, false);
            $st = db()->prepare("SELECT DISTINCT u.id, u.nickname, u.username FROM social_messages m INNER JOIN users u ON u.id = m.user_id WHERE m.group_id = ? AND u.role = 'admin'");
            $st->execute([$gid]);
            $st2 = db()->query("SELECT COUNT(DISTINCT user_id) AS c FROM social_messages WHERE group_id = " . $gid);
            $g['member_count'] = (int)($st2->fetch()['c'] ?? 0);
            $muted = my_muted_ids();
            $unread = my_unread_map();
            $mentions = my_mention_map();
            $lasts = group_last_messages([$gid]);
            json_out([
                'group'  => social_group_public(
                    $g,
                    isset($muted[$gid]) ? 1 : 0,
                    (int)($unread[$gid]['count'] ?? 0),
                    (int)($unread[$gid]['first_id'] ?? 0),
                    $lasts[$gid] ?? null,
                    $mentions[$gid] ?? []
                ),
                'notice' => (string)($g['notice'] ?? ''),
                'admins' => $st->fetchAll(),
            ]);

        case 'social_messages':
            $me = current_user_or_401();
            $gid = (int)param('group_id', 0);
            $after = (int)param('after_id', 0);
            $before = (int)param('before_id', 0);
            $around = (int)param('around_id', 0);
            $limit = min(50, max(1, (int)param('limit', 30)));
            social_group_or_404($gid, false);
            $sql = "SELECT m.*, u.nickname, u.username, u.avatar, u.role, u.tags
                    FROM social_messages m LEFT JOIN users u ON u.id = m.user_id
                    WHERE m.group_id = ?";
            if ($around > 0) {
                // 从通知点进来时用: 目标消息放中间, 前后各取一半, 这样既看得到上下文也能继续往下翻
                $half = (int)max(5, floor($limit / 2));
                $st = db()->prepare($sql . ' AND m.id <= ? ORDER BY m.id DESC LIMIT ' . $half);
                $st->execute([$gid, $around]);
                $before = array_reverse($st->fetchAll());
                $st = db()->prepare($sql . ' AND m.id > ? ORDER BY m.id ASC LIMIT ' . $half);
                $st->execute([$gid, $around]);
                $rows = array_merge($before, $st->fetchAll());
            } elseif ($before > 0) {
                // 往上翻: 取比 before_id 更早的一页 (先按 id 倒序取 limit 条, 再反转成正序返回)
                $st = db()->prepare($sql . ' AND m.id < ? ORDER BY m.id DESC LIMIT ' . $limit);
                $st->execute([$gid, $before]);
                $rows = array_reverse($st->fetchAll());
            } elseif ($after > 0) {
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
                // 引用回复: 一次性把被引用的原消息查出来挂到每条消息上 (避免 N+1)
                $qids = [];
                foreach ($rows as $r) {
                    $q = (int)($r['quote_id'] ?? 0);
                    if ($q > 0) $qids[$q] = true;
                }
                if ($qids) {
                    $qk = array_keys($qids);
                    $in2 = implode(',', array_fill(0, count($qk), '?'));
                    $st = db()->prepare('SELECT m.id, m.content, m.user_id, u.nickname
                                         FROM social_messages m LEFT JOIN users u ON u.id = m.user_id
                                         WHERE m.id IN (' . $in2 . ')');
                    $st->execute($qk);
                    $qmap = [];
                    foreach ($st->fetchAll() as $q) {
                        $qn = (string)($q['nickname'] ?? '');
                        $qmap[(int)$q['id']] = [$qn !== '' ? $qn : ('用户' . (int)$q['user_id']), (string)$q['content']];
                    }
                    foreach ($rows as $i => $r) {
                        $q = (int)($r['quote_id'] ?? 0);
                        if ($q > 0 && isset($qmap[$q])) {
                            $rows[$i]['quote_nickname'] = $qmap[$q][0];
                            $rows[$i]['quote_content'] = $qmap[$q][1];
                        }
                    }
                }
            }
            // 未读定位: 返回「第一条未读消息 id」和未读条数, 客户端据此把列表初始化到那里并高亮;
            // 读取本身不改已读位置, 由客户端展示完后调 social_read 标记 (避免刷新一下就误标已读)
            $unread = my_unread_map();
            // 是否还有更早的消息: 客户端上滑到顶时用它决定还要不要继续请求
            $firstId = 0;
            foreach ($rows as $r0) { $firstId = (int)$r0['id']; break; }
            $hasMoreBefore = false;
            if ($firstId > 0) {
                $st = db()->prepare('SELECT COUNT(*) FROM social_messages WHERE group_id = ? AND id < ?');
                $st->execute([$gid, $firstId]);
                $hasMoreBefore = ((int)$st->fetchColumn()) > 0;
            }
            json_out([
                'list'            => array_map('social_msg_public', $rows),
                'has_more'        => count($rows) >= $limit,
                'has_more_before' => $hasMoreBefore,
                'unread'          => (int)($unread[$gid]['count'] ?? 0),
                'first_unread_id' => (int)($unread[$gid]['first_id'] ?? 0),
                'my_id'           => (int)$me['id'],
            ]);

        case 'social_read':
            // 把某群标记为已读 (last_id 不传则取该群最新一条消息 id)
            $me = current_user_or_401();
            $gid = (int)param('group_id', 0);
            social_group_or_404($gid, false);
            $lastId = (int)param('last_id', 0);
            if ($lastId <= 0) {
                $st = db()->prepare('SELECT COALESCE(MAX(id), 0) FROM social_messages WHERE group_id = ?');
                $st->execute([$gid]);
                $lastId = (int)$st->fetchColumn();
            }
            db()->prepare('INSERT INTO social_reads (user_id, group_id, last_read_id, updated_at)
                           VALUES (?, ?, ?, NOW())
                           ON DUPLICATE KEY UPDATE last_read_id = GREATEST(last_read_id, VALUES(last_read_id)), updated_at = NOW()')
                ->execute([(int)$me['id'], $gid, $lastId]);
            // 回读真实已读位置 (GREATEST 保证不会回退, 传更小的 last_id 时库里仍是较大的值)
            $st = db()->prepare('SELECT last_read_id FROM social_reads WHERE user_id = ? AND group_id = ?');
            $st->execute([(int)$me['id'], $gid]);
            $stored = (int)$st->fetchColumn();
            json_out(['ok' => true, 'last_read_id' => $stored]);

        case 'social_image_upload':
            // 群聊图片: 先上传拿到对象存储地址, 再带着这个地址调 social_send 发消息
            $me = current_user_or_401();
            if ((int)$me['is_active'] !== 1) json_error('你已被封禁', 403);
            if (empty($_FILES['file'])) json_error('未收到文件');
            $file = $_FILES['file'];
            if ((int)($file['size'] ?? 0) > 10 * 1024 * 1024) json_error('图片不能超过 10MB');
            $raw = @file_get_contents($file['tmp_name']);
            if ($raw === false || $raw === '') json_error('读取文件失败');
            $info = @getimagesizefromstring($raw);
            if (!$info) json_error('这不是一张有效的图片');
            $mimeMap = ['image/jpeg' => 'jpg', 'image/png' => 'png', 'image/webp' => 'webp'];
            $realMime = strtolower((string)($info['mime'] ?? ''));
            if (!isset($mimeMap[$realMime])) json_error('只支持 jpg / png / webp 图片');
            $ext = $mimeMap[$realMime];
            $w = (int)$info[0]; $h = (int)$info[1];
            $raw = compress_image($raw, $ext, 1600, 82);
            $mime = $ext === 'jpg' ? 'image/jpeg' : ($ext === 'png' ? 'image/png' : 'image/webp');
            $key = s3_key('chat', $ext);
            if (!s3_upload_bytes($raw, $key, $mime)) json_error('上传失败, 请稍后重试');
            json_out(['url' => S3_PUBLIC_URL . '/' . $key, 'width' => $w, 'height' => $h]);

        case 'video_config':
            // 客户端只读视频配置 (只吐 enabled / max_mb, 后台字段不外泄); 无需登录, 前端启动即可拉
            $vcfg = video_config();
            json_out(['enabled' => (int)$vcfg['enabled'], 'max_mb' => (int)$vcfg['max_mb']]);

        case 'social_video_upload':
            // 群聊视频: 先上传拿到对象存储地址, 再带着这个地址调 social_send 发视频消息
            // 上传成功后顺带跑一次容量/天数清理 (契约 Wave 2)
            $me = current_user_or_401();
            if ((int)$me['is_active'] !== 1) json_error('你已被封禁', 403);
            $vcfg = video_config();
            if ((int)$vcfg['enabled'] !== 1) json_error('视频消息功能未开启');
            if (empty($_FILES['file'])) {
                // 请求体超过 post_max_size 时 PHP 连 $_FILES 都不会建, 这里给一句能看懂的话
                $clen = (int)($_SERVER['CONTENT_LENGTH'] ?? 0);
                if ($clen > 0) json_error('文件超过服务器上传限制, 请压缩后再传');
                json_error('未收到文件');
            }
            $file = $_FILES['file'];
            if (!empty($file['error'])) {
                $emap = [
                    1 => '视频超过服务器上传限制',
                    2 => '视频超过服务器上传限制',
                    3 => '文件只上传了一部分, 请重试',
                    4 => '未收到文件',
                    6 => '服务器缺少临时目录',
                    7 => '服务器写入失败',
                    8 => '上传被服务器拦截',
                ];
                json_error($emap[(int)$file['error']] ?? ('上传失败 (代码 ' . (int)$file['error'] . ')'));
            }
            $vmaxBytes = (int)$vcfg['max_mb'] * 1024 * 1024;
            if ((int)($file['size'] ?? 0) > $vmaxBytes) {
                json_error('视频不能超过 ' . (int)$vcfg['max_mb'] . 'MB');
            }
            $raw = @file_get_contents($file['tmp_name']);
            if ($raw === false || $raw === '') json_error('读取文件失败');
            $ext = video_detect_ext($raw, (string)($file['name'] ?? ''));
            if ($ext === '') json_error('只支持 mp4 / mov / mkv / webm 视频');
            list($vw, $vh, $vd, $vs) = video_meta_params();
            $key = s3_key('chat', $ext);
            if (!s3_upload_bytes($raw, $key, video_ctype($ext))) json_error('上传失败, 请稍后重试');
            $vclean = video_cleanup_if_needed();
            json_out([
                'url'      => S3_PUBLIC_URL . '/' . $key,
                'size'     => strlen($raw),
                'width'    => $vw,
                'height'   => $vh,
                'duration' => $vd,
                'cleaned'  => (int)$vclean['deleted'],
            ]);

        case 'social_send':
            $me = current_user_or_401();
            if ((int)$me['is_active'] !== 1) json_error('你已被封禁', 403);
            $gid = (int)param('group_id', 0);
            $g = social_group_or_404($gid, false);
            // 必须先加入群聊才能发言: 非管理员且不是群成员 -> 403 (放在禁言判定之前, 契约 A2)
            if (($me['role'] ?? '') !== 'admin' && !social_is_member($gid, (int)$me['id'])) {
                json_error('请先加入群聊再发言', 403);
            }
            // 全员禁言: 非管理员一律拦截 (管理员不受限)
            if ((int)($g['all_muted'] ?? 0) === 1 && ($me['role'] ?? '') !== 'admin') {
                json_error('群主已开启全体禁言, 暂时不能发言', 403);
            }
            // 管理员禁言: 群里 / 全站被禁言的用户不能发言 (管理员不受限)
            if (($me['role'] ?? '') !== 'admin') {
                $muteState = user_mute_state((int)$me['id'], $gid);
                if ($muteState) {
                    $leftTxt = mute_left_text($muteState);
                    $why = trim((string)($muteState['reason'] ?? ''));
                    json_error('你已被禁言' . ($leftTxt !== '' ? ' (' . $leftTxt . ')' : '')
                        . ($why !== '' ? ', 原因: ' . $why : ''), 403);
                }
            }
            $content = trim((string)param('content', ''));
            $image = trim((string)param('image', ''));
            if ($image !== '' && strpos($image, S3_PUBLIC_URL . '/chat/') !== 0) json_error('图片地址不合法');
            $imgW = max(0, (int)param('image_w', 0));
            $imgH = max(0, (int)param('image_h', 0));
            // 视频消息 (契约 Wave 2): 地址前缀强校验与图片一致, 尺寸/时长/大小只做范围兜底
            $video = trim((string)param('video', ''));
            if ($video !== '' && strpos($video, S3_PUBLIC_URL . '/chat/') !== 0) json_error('视频地址不合法');
            // 视频消息功能关闭时不能再发出视频消息 (契约 Wave 2: 未开启一律中文报错)
            if ($video !== '' && (int)video_config()['enabled'] !== 1) json_error('视频消息功能未开启');
            list($vidW, $vidH, $vidDur, $vidSize) = video_meta_params();
            if ($video !== '' && !db_has_column('social_messages', 'video')) {
                json_error('服务端未完成视频迁移, 请联系管理员');
            }
            $msgType = $video !== '' ? 'video' : '';
            if ($content === '' && $image === '' && $video === '') json_error('消息内容不能为空');
            if (mb_strlen($content) > 500) json_error('消息不能超过 500 个字');
            // 图片/视频消息在通知里统一显示 [图片] / [视频]
            $notifyText = $content !== '' ? mb_substr($content, 0, 80) : ($video !== '' ? '[视频]' : '[图片]');
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
            // @所有人: 只有管理员能发, 可以由 at_all=1 指定, 也可以在内容里写 @所有人
            $isAdminSender = ($me['role'] ?? '') === 'admin';
            $atAll = $isAdminSender && (
                (int)param('at_all', 0) === 1 || mb_strpos($content, '@所有人') !== false
            );
            // at_users 里用 0 作为 "所有人" 的标记位 (真实用户 id 都 > 0)
            $atStore = $atAll ? array_merge([0], $at) : $at;
            // 引用回复: 只接受同群存在的消息, 否则忽略
            $quoteId = (int)param('quote_id', 0);
            if ($quoteId > 0) {
                $st = db()->prepare('SELECT id FROM social_messages WHERE id = ? AND group_id = ?');
                $st->execute([$quoteId, $gid]);
                if (!$st->fetchColumn()) $quoteId = 0;
            }
            // 列先探测再拼 INSERT: 迁移没跑时自动退回旧的列组合 (图片/文字消息行为一字不改)
            $scols = ['group_id', 'user_id', 'content', 'image', 'image_w', 'image_h', 'at_users', 'quote_id'];
            $svals = [$gid, (int)$me['id'], $content, $image, $imgW, $imgH, implode(',', $atStore), $quoteId];
            if (db_has_column('social_messages', 'video')) {
                array_push($scols, 'video', 'video_w', 'video_h', 'video_duration', 'video_size');
                array_push($svals, $video, $vidW, $vidH, $vidDur, $vidSize);
            }
            if (db_has_column('social_messages', 'msg_type')) { $scols[] = 'msg_type'; $svals[] = $msgType; }
            $scols[] = 'is_recalled';
            $svals[] = 0;
            db()->prepare('INSERT INTO social_messages (' . implode(', ', $scols) . ') VALUES ('
                . implode(', ', array_fill(0, count($scols), '?')) . ')')->execute($svals);
            $mid = (int)db()->lastInsertId();
            if ($atAll) {
                $st = db()->prepare('SELECT id FROM users WHERE is_active = 1 AND id <> ?');
                $st->execute([(int)$me['id']]);
                foreach ($st->fetchAll() as $t) {
                    notify_push((int)$t['id'], '「' . $g['name'] . '」有人 @了所有人', $notifyText, 'social');
                }
            } elseif ($at) {
                $in = implode(',', array_fill(0, count($at), '?'));
                $st = db()->prepare('SELECT id FROM users WHERE id IN (' . $in . ') AND is_active = 1');
                $st->execute($at);
                foreach ($st->fetchAll() as $t) {
                    // link 里带上群 id 与消息 id, 客户端点通知能直接跳进群并定位到这条消息
                    notify_push((int)$t['id'], '有人在「' . $g['name'] . '」@了你', $notifyText, 'social', 'msg:' . $gid . ':' . $mid);
                }
            }
            // 普通群消息提醒: 给群内其他成员推一条「「X」新消息」(同一未读合并, 不刷屏)。
            // 开过免打扰的跳过; 已经被 @ 单独提醒过的人不再重复推。
            // @所有人 时上面已经给所有人推过通知, 这里不再重复。
            if (!$atAll) {
                $atSet = [];
                foreach ($at as $aid) { $atSet[(int)$aid] = true; }
                $mutedIds = mute_user_ids($gid);
                $senderName = (string)(($me['nickname'] ?? '') !== '' ? $me['nickname'] : ($me['username'] ?? ''));
                $brief = $content !== '' ? mb_substr($content, 0, 60) : ($video !== '' ? '[视频]' : '[图片]');
                $st = db()->prepare('SELECT id FROM users WHERE is_active = 1 AND id <> ?');
                $st->execute([(int)$me['id']]);
                foreach ($st->fetchAll(PDO::FETCH_COLUMN) as $uid) {
                    $uid = (int)$uid;
                    if (isset($atSet[$uid])) continue;
                    if (isset($mutedIds[$uid])) continue;
                    notify_merge($uid, '「' . $g['name'] . '」新消息', $senderName . ': ' . $brief, 'social', 'msg:' . $gid . ':' . $mid);
                }
            }
            json_out(['id' => $mid, 'at_all' => $atAll ? 1 : 0, 'quote_id' => $quoteId, 'created_at' => date('Y-m-d H:i:s')]);

        // ============ SSE 实时消息推送 (契约 A1~A4) ============
        // 未登录: 先返回普通 JSON 401, 不进流模式 (契约 A1)
        case 'stream':
            $me = current_user();
            if (!$me) json_error('登录已失效', 401);
            // case 里只做鉴权和取参数, 流循环逻辑全部在 sse_run() 里 (便于 review)
            sse_run($me, (int)param('pm_id', 0), (int)param('group_id', 0));
            exit;

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
            // 公告是重要信息, 开了免打扰也提醒 (和 QQ 一样)
            if ($notice !== '') {
                $st = db()->prepare('SELECT id FROM users WHERE is_active = 1 AND id <> ?');
                $st->execute([(int)current_user()['id']]);
                foreach ($st->fetchAll(PDO::FETCH_COLUMN) as $uid) {
                    notify_merge((int)$uid, '「' . $g['name'] . '」群公告更新', mb_substr($notice, 0, 80), 'social', 'notice:' . $gid);
                }
            }
            json_out(['ok' => true, 'notified' => $notice !== '']);

        // 群全体禁言 (仅管理员可设置): 开启后该群除管理员外都不能发言
        case 'social_group_allmute_set':
            require_admin();
            $gid = (int)param('group_id', 0);
            if ($gid <= 0) json_error('参数错误');
            $muted = (int)param('muted', 0) === 1 ? 1 : 0;
            $stg = db()->prepare('SELECT id FROM social_groups WHERE id = ?');
            $stg->execute([$gid]);
            if (!$stg->fetchColumn()) json_error('群组不存在');
            db()->prepare('UPDATE social_groups SET all_muted = ? WHERE id = ?')->execute([$muted, $gid]);
            json_out(['group_id' => $gid, 'all_muted' => $muted]);

        // ============ 群成员: 加入 / 退出 / 群图片 / 群成员列表 (契约 A3) ============

        // 加入群聊: 幂等 —— 已是成员返回 already=1 (不加成员、不写系统消息)
        case 'social_group_join':
            $me = current_user_or_401();
            $gid = (int)param('group_id', 0);
            if ($gid <= 0) json_error('参数错误');
            social_group_or_404($gid, false);
            $already = 0;
            $ins = db()->prepare('INSERT IGNORE INTO social_group_members (group_id, user_id, role, joined_at) VALUES (?, ?, ?, NOW())');
            $ins->execute([$gid, (int)$me['id'], ($me['role'] ?? '') === 'admin' ? 'owner' : 'member']);
            if ($ins->rowCount() <= 0) {
                // 已经是成员 (也可能是并发下被别人先插入)
                $already = 1;
            } else {
                // 新加入: 落一条系统消息 (昵称取 users.nickname, 空则 username; at_users = NULL)
                social_system_message($gid, (int)$me['id'], social_user_display_name($me) . '加入了群聊');
            }
            json_out([
                'group_id'     => $gid,
                'joined'       => 1,
                'already'      => $already,
                'member_count' => social_member_count($gid),
            ]);

        // 退出群聊: 删成员行 + 系统消息「xxx退出了群聊」(最后一个成员 / owner 也允许退出, 不清群)
        case 'social_group_leave':
            $me = current_user_or_401();
            $gid = (int)param('group_id', 0);
            if ($gid <= 0) json_error('参数错误');
            social_group_or_404($gid, false);
            $del = db()->prepare('DELETE FROM social_group_members WHERE group_id = ? AND user_id = ?');
            $del->execute([$gid, (int)$me['id']]);
            if ($del->rowCount() <= 0) json_error('你还不是群成员');
            social_system_message($gid, (int)$me['id'], social_user_display_name($me) . '退出了群聊');
            json_out(['group_id' => $gid, 'left' => 1, 'member_count' => social_member_count($gid)]);

        // 群图片列表 (群相册): 未撤回且有图片的消息, 按 id 倒序分页; 返回 image 原图地址
        case 'social_group_images':
            $gid = (int)param('group_id', 0);
            if ($gid <= 0) json_error('参数错误');
            social_group_or_404($gid, false);
            $page = max(1, (int)param('page', 1));
            $ps   = min(60, max(1, (int)param('page_size', 30)));
            $off  = ($page - 1) * $ps;
            $st = db()->prepare("SELECT COUNT(*) FROM social_messages m
                                  WHERE m.group_id = ? AND m.is_recalled = 0 AND m.image <> ''");
            $st->execute([$gid]);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare("SELECT m.id, m.user_id, m.image, m.image_w, m.image_h, m.created_at,
                                        u.nickname, u.username, u.avatar
                                   FROM social_messages m LEFT JOIN users u ON u.id = m.user_id
                                  WHERE m.group_id = ? AND m.is_recalled = 0 AND m.image <> ''
                                  ORDER BY m.id DESC LIMIT " . $ps . ' OFFSET ' . $off);
            $st->execute([$gid]);
            $list = [];
            foreach ($st->fetchAll() as $r) {
                $nick = trim((string)($r['nickname'] ?? ''));
                $list[] = [
                    'id'         => (int)$r['id'],
                    'user_id'    => (int)$r['user_id'],
                    'nickname'   => $nick !== '' ? $nick : ('用户' . (int)$r['user_id']),
                    'username'   => (string)($r['username'] ?? ''),
                    'avatar'     => (string)($r['avatar'] ?? ''),
                    'image'      => (string)($r['image'] ?? ''),
                    'image_w'    => (int)($r['image_w'] ?? 0),
                    'image_h'    => (int)($r['image_h'] ?? 0),
                    'created_at' => (string)($r['created_at'] ?? ''),
                ];
            }
            json_out([
                'list'      => $list,
                'page'      => $page,
                'page_size' => $ps,
                'total'     => $total,
                'has_more'  => ($off + count($list)) < $total,
            ]);

        // 消息免打扰 (每个用户对自己生效)
        case 'social_mute_set':
            $me = current_user_or_401();
            $gid = (int)param('group_id', 0);
            social_group_or_404($gid, false);
            $muted = (int)param('muted', 0) === 1 ? 1 : 0;
            if ($muted) {
                db()->prepare('INSERT IGNORE INTO social_mutes (group_id, user_id, created_at) VALUES (?, ?, NOW())')
                    ->execute([$gid, (int)$me['id']]);
            } else {
                db()->prepare('DELETE FROM social_mutes WHERE group_id = ? AND user_id = ?')
                    ->execute([$gid, (int)$me['id']]);
            }
            json_out(['group_id' => $gid, 'muted' => $muted]);

        // 群成员列表 (可搜索): keyword 同时匹配 nickname / username (契约 A3)
        // 排序: 群主 -> 管理员 -> 普通成员, 同角色按加入时间升序
        case 'social_group_members':
            $me  = current_user();          // 游客也能看, is_member = 0
            $gid = (int)param('group_id', 0);
            if ($gid <= 0) json_error('参数错误');
            social_group_or_404($gid, false);
            $kw   = trim((string)param('keyword', ''));
            $page = max(1, (int)param('page', 1));
            $ps   = min(60, max(1, (int)param('page_size', 30)));
            $off  = ($page - 1) * $ps;
            $where = ['m.group_id = ?'];
            $args  = [$gid];
            if ($kw !== '') {
                $where[] = '(u.nickname LIKE ? OR u.username LIKE ?)';
                $args[]  = '%' . $kw . '%';
                $args[]  = '%' . $kw . '%';
            }
            $cond = ' WHERE ' . implode(' AND ', $where);
            $st = db()->prepare('SELECT COUNT(*) FROM social_group_members m JOIN users u ON u.id = m.user_id' . $cond);
            $st->execute($args);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare('SELECT u.*, m.role AS group_role, m.joined_at
                                   FROM social_group_members m JOIN users u ON u.id = m.user_id'
                . $cond . " ORDER BY FIELD(m.role, 'owner', 'admin', 'member'), m.joined_at ASC, m.id ASC
                          LIMIT " . $ps . ' OFFSET ' . $off);
            $st->execute($args);
            $list = [];
            foreach ($st->fetchAll() as $u) {
                $mu   = user_mute_state((int)$u['id'], $gid);
                $item = user_brief($u);
                $item['role']        = (string)$u['group_role'];                 // 群角色: owner/admin/member
                $item['group_role']  = (string)$u['group_role'];
                $item['joined_at']   = (string)($u['joined_at'] ?? '');
                // 兼容旧客户端 @ 选人面板 (SocialScreen.kt 里读 role == 'admin' 判全局管理员)
                $item['global_role'] = (string)($u['role'] ?? 'user');
                $item['is_admin']    = ((string)($u['role'] ?? '')) === 'admin' ? 1 : 0;
                $item['muted']       = $mu ? 1 : 0;
                $item['mute_left']   = $mu ? mute_left_text($mu) : '';
                $item['mute_reason'] = $mu ? (string)($mu['reason'] ?? '') : '';
                $list[] = $item;
            }
            $meId = $me ? (int)$me['id'] : 0;
            json_out([
                'list'         => $list,
                'page'         => $page,
                'page_size'    => $ps,
                'total'        => $total,
                'is_member'    => $meId > 0 ? social_is_member($gid, $meId) : 0,
                'member_count' => social_member_count($gid),
            ]);

        // ============ 群内禁言 (管理员) ============
        case 'admin_user_mute':
            require_admin();
            $uid = (int)param('user_id', 0);
            $gid = (int)param('group_id', 0);
            $minutes = (int)param('minutes', 60);
            $reason = trim((string)param('reason', ''));
            if (mb_strlen($reason) > 60) json_error('禁言原因不能超过 60 个字');
            $st = db()->prepare('SELECT id, nickname, username, role FROM users WHERE id = ?');
            $st->execute([$uid]);
            $u = $st->fetch();
            if (!$u) json_error('用户不存在');
            if (($u['role'] ?? '') === 'admin') json_error('不能禁言管理员');
            if ($gid > 0) social_group_or_404($gid, false);
            if ($minutes < 0) $minutes = 0;
            if ($minutes > 432000) $minutes = 432000; // 上限 300 天, 0 = 永久
            $until = $minutes > 0 ? date('Y-m-d H:i:s', time() + $minutes * 60) : null;
            db()->prepare('INSERT INTO social_user_mutes (group_id, user_id, until_at, reason, created_by, created_at)
                           VALUES (?, ?, ?, ?, ?, NOW())
                           ON DUPLICATE KEY UPDATE until_at = VALUES(until_at), reason = VALUES(reason),
                                                   created_by = VALUES(created_by), created_at = NOW()')
                ->execute([$gid, $uid, $until, $reason, (int)current_user()['id']]);
            $mute = ['until_at' => $until, 'reason' => $reason];
            $leftTxt = mute_left_text($mute);
            $gname = '';
            if ($gid > 0) {
                $st = db()->prepare('SELECT name FROM social_groups WHERE id = ?');
                $st->execute([$gid]);
                $gname = (string)($st->fetchColumn() ?: '');
            }
            notify_push($uid, '你已被禁言',
                ($gname !== '' ? '在「' . $gname . '」' : '全站') . '被管理员禁言' .
                ($leftTxt !== '' ? ', ' . $leftTxt : '') . ($reason !== '' ? ', 原因: ' . $reason : ''),
                'admin');
            json_out(['user_id' => $uid, 'group_id' => $gid, 'until_at' => (string)($until ?? ''),
                      'left_text' => $leftTxt, 'reason' => $reason]);

        case 'admin_user_unmute':
            require_admin();
            $uid = (int)param('user_id', 0);
            $gid = (int)param('group_id', 0);
            if ($uid <= 0) json_error('参数错误');
            $del = db()->prepare('DELETE FROM social_user_mutes WHERE user_id = ? AND group_id = ?');
            $del->execute([$uid, $gid]);
            $n = $del->rowCount();
            if ($n > 0) {
                notify_push($uid, '禁言已解除', '管理员已解除你的禁言, 现在可以正常发言了', 'admin');
            }
            json_out(['user_id' => $uid, 'group_id' => $gid, 'removed' => $n]);

        // ============ 消息通知 ============
        // ============ 抽奖 (客户端) ============
        case 'lottery_info':
            $me = current_user_or_401();
            $cfg = lottery_config();
            $st = db()->prepare("SELECT p.id, p.name, p.card_type, p.weight,
                    (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id AND c.user_id = 0) AS left_cnt
                FROM lottery_prizes p WHERE p.is_active = 1 ORDER BY p.sort_order ASC, p.id ASC");
            $st->execute();
            $prizes = [];
            foreach ($st->fetchAll() as $p) {
                $leftCnt = (int)$p['left_cnt'];
                if ($leftCnt <= 0) continue;
                $prizes[] = [
                    'id' => (int)$p['id'], 'name' => (string)$p['name'],
                    'card_type' => (string)$p['card_type'], 'left' => $leftCnt,
                    'weight' => (int)($p['weight'] ?? 0) > 0 ? (int)$p['weight'] : 100,
                ];
            }
            if ((int)$cfg['show_prizes'] !== 1) $prizes = [];
            $st = db()->prepare('SELECT d.id, d.prize_id, d.code, d.created_at, p.name AS prize_name, p.card_type
                FROM lottery_draws d LEFT JOIN lottery_prizes p ON p.id = d.prize_id
                WHERE d.user_id = ? ORDER BY d.id DESC LIMIT 20');
            $st->execute([(int)$me['id']]);
            $records = [];
            foreach ($st->fetchAll() as $r) {
                $records[] = [
                    'id' => (int)$r['id'], 'prize_id' => (int)$r['prize_id'],
                    'prize_name' => (string)($r['prize_name'] ?? ''), 'card_type' => (string)($r['card_type'] ?? ''),
                    'code' => (string)$r['code'], 'created_at' => (string)$r['created_at'],
                ];
            }
            json_out([
                'enabled'        => (int)$cfg['enabled'],
                'title'          => (string)$cfg['title'],
                'content'        => (string)$cfg['content'],
                'per_user_limit' => (int)$cfg['per_user_limit'],
                'my_quota'       => lottery_left_for($me),
                'my_drawn'       => lottery_drawn_count((int)$me['id']),
                'daily_limit'    => (int)$cfg['daily_limit'],
                'my_today_drawn' => lottery_drawn_count_today((int)$me['id']),
                'my_today_left'  => lottery_daily_left_for($me),
                'server_time'    => date('Y-m-d H:i:s', lottery_cfg_ts()),
                'window'         => lottery_window_state($cfg),
                'daily_reset_time' => (string)$cfg['daily_reset_time'],
                'week_limit'     => (int)$cfg['week_limit'],
                'my_week_drawn'  => lottery_drawn_count_week((int)$me['id']),
                'my_week_left'   => lottery_weekly_left_for($me),
                'cooldown_seconds' => (int)$cfg['cooldown_seconds'],
                'cooldown_left'  => lottery_cooldown_left($me),
                'show_prizes'    => (int)$cfg['show_prizes'],
                'show_stock'     => (int)$cfg['show_stock'],
                'daily_total_limit' => (int)$cfg['daily_total_limit'],
                'daily_total_left'  => lottery_daily_total_left($cfg),
                'success_text'   => (string)$cfg['success_text'],
                'empty_text'     => (string)$cfg['empty_text'],
                'start_date'     => (string)$cfg['start_date'],
                'end_date'       => (string)$cfg['end_date'],
                'windows_enabled' => (int)$cfg['windows_enabled'],
                'windows'        => $cfg['windows'],
                'prizes'         => $prizes,
                'records'        => $records,
            ]);

        case 'lottery_draw':
            $me = current_user_or_401();
            $cfg = lottery_config();
            // 校验顺序固定: 开关 -> 日期范围 -> 开放时段 -> 冷却 -> 全站每日总量 -> 每日 -> 每周 -> 总次数 -> 池子
            if ((int)$cfg['enabled'] !== 1) json_error('抽奖活动已关闭');
            $range = lottery_date_range_state($cfg, lottery_cfg_ts());
            if ($range === 1) json_error('抽奖活动还没开始');
            if ($range === 2) json_error('抽奖活动已经结束');
            if (!lottery_window_ts_open($cfg, lottery_cfg_ts())) {
                $win = lottery_window_state($cfg);
                $msg = '现在不在抽奖时间内';
                if ((string)$win['next_open_at'] !== '') $msg .= ', 下次开放: ' . $win['next_open_at'];
                json_error($msg);
            }
            $cdLeft = lottery_cooldown_left($me);
            if ($cdLeft > 0) json_error('抽得太快啦, 请 ' . $cdLeft . ' 秒后再试');
            $totalLeft = lottery_daily_total_left($cfg);
            if ($totalLeft === 0) json_error('今天的奖品已经发完了, 明天再来');
            if (lottery_daily_left_for($me) === 0) json_error('今天的抽奖次数已用完, 明天再来');
            if (lottery_weekly_left_for($me) === 0) json_error('本周的抽奖次数已用完, 下周再来');
            if (lottery_left_for($me) <= 0) json_error('你的抽奖次数已用完');
            $got = lottery_draw_once((int)$me['id']);
            if ((int)$cfg['notify_winner'] === 1) {
                try {
                    db()->prepare('INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)')
                        ->execute([
                            (int)$me['id'],
                            '恭喜抽中 ' . $got['prize_name'],
                            "你的卡密: " . $got['code'] . "\n(长按可复制, 也可以随时在「我的 → 消息中心」查看)",
                            'lottery',
                            '',
                        ]);
                } catch (Exception $e) { error_log('[lottery notify] ' . $e->getMessage()); }
            }
            json_out([
                'draw_id'    => (int)$got['id'],
                'prize_id'   => (int)$got['prize_id'],
                'prize_name' => (string)$got['prize_name'],
                'card_type'  => (string)$got['card_type'],
                'code'       => (string)$got['code'],
                'left'       => lottery_left_for($me),
                'my_today_left' => lottery_daily_left_for($me),
                'my_week_left'  => lottery_weekly_left_for($me),
                'daily_total_left' => lottery_daily_total_left($cfg),
                'cooldown_seconds' => (int)$cfg['cooldown_seconds'],
                'cooldown_left'    => lottery_cooldown_left($me),
            ]);

        case 'lottery_records':
            $me = current_user_or_401();
            $page = max(1, (int)param('page', 1));
            $ps = min(50, max(1, (int)param('page_size', 20)));
            $st = db()->prepare('SELECT COUNT(*) FROM lottery_draws WHERE user_id = ?');
            $st->execute([(int)$me['id']]);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare('SELECT d.id, d.prize_id, d.code, d.created_at, p.name AS prize_name, p.card_type
                FROM lottery_draws d LEFT JOIN lottery_prizes p ON p.id = d.prize_id
                WHERE d.user_id = ? ORDER BY d.id DESC LIMIT ' . $ps . ' OFFSET ' . (($page - 1) * $ps));
            $st->execute([(int)$me['id']]);
            $list = [];
            foreach ($st->fetchAll() as $r) {
                $list[] = [
                    'id' => (int)$r['id'], 'prize_id' => (int)$r['prize_id'],
                    'prize_name' => (string)($r['prize_name'] ?? ''), 'card_type' => (string)($r['card_type'] ?? ''),
                    'code' => (string)$r['code'], 'created_at' => (string)$r['created_at'],
                ];
            }
            json_out(['list' => $list, 'total' => $total, 'page' => $page, 'page_size' => $ps]);

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
            // 分类未读数 (系统通知 / 管理员公告 / 群聊相关), 客户端「消息」页要显示「新消息 N 条」
            $st = db()->prepare('SELECT type, COUNT(*) AS c FROM notifications WHERE user_id = ? AND is_read = 0 GROUP BY type');
            $st->execute([(int)$me['id']]);
            $byType = [];
            foreach ($st->fetchAll() as $r2) { $byType[(string)$r2['type']] = (int)$r2['c']; }
            $off = ($page - 1) * $ps;
            $st = db()->prepare('SELECT * FROM notifications WHERE ' . $cond . ' ORDER BY id DESC LIMIT ' . $ps . ' OFFSET ' . $off);
            $st->execute([(int)$me['id']]);
            json_out([
                'list' => array_map('notify_public', $st->fetchAll()),
                'total' => $total, 'unread' => $unread, 'unread_by_type' => $byType,
                'page' => $page, 'page_size' => $ps,
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
            // 全体禁言: 仅当调用方显式提交 all_muted 时才写库 (一键启用/停用不会误清)
            $hasAllMuted = has_param('all_muted');
            $allMuted = (int)param('all_muted', 0) === 1 ? 1 : 0;
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
                if ($hasAllMuted) {
                    db()->prepare('UPDATE social_groups SET all_muted = ? WHERE id = ?')->execute([$allMuted, $id]);
                }
                json_out(['id' => $id]);
            }
            db()->prepare('INSERT INTO social_groups (name, icon, description, sort_order, is_active, notice) VALUES (?, ?, ?, ?, ?, ?)')
                ->execute([$name, $icon, $desc, $sort, $active, $notice]);
            $newGid = (int)db()->lastInsertId();
            if ($hasAllMuted) {
                db()->prepare('UPDATE social_groups SET all_muted = ? WHERE id = ?')->execute([$allMuted, $newGid]);
            }
            json_out(['id' => $newGid]);

        case 'admin_group_delete':
            require_admin();
            $id = (int)param('id', 0);
            if ($id <= 0) json_error('参数错误');
            db()->prepare('DELETE FROM social_messages WHERE group_id = ?')->execute([$id]);
            db()->prepare('DELETE FROM social_groups WHERE id = ?')->execute([$id]);
            json_out(['ok' => true]);

        // ============ 后台: 群成员管理 (契约 A5) ============
        case 'admin_group_members':
            require_admin();
            $gid = (int)param('group_id', 0);
            if ($gid <= 0) json_error('参数错误');
            social_group_or_404($gid, false);
            $kw   = trim((string)param('keyword', ''));
            $page = max(1, (int)param('page', 1));
            $ps   = min(60, max(1, (int)param('page_size', 30)));
            $off  = ($page - 1) * $ps;
            $where = ['m.group_id = ?'];
            $args  = [$gid];
            if ($kw !== '') {
                $where[] = '(u.nickname LIKE ? OR u.username LIKE ?)';
                $args[]  = '%' . $kw . '%';
                $args[]  = '%' . $kw . '%';
            }
            $cond = ' WHERE ' . implode(' AND ', $where);
            $st = db()->prepare('SELECT COUNT(*) FROM social_group_members m JOIN users u ON u.id = m.user_id' . $cond);
            $st->execute($args);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare('SELECT u.*, m.role AS group_role, m.joined_at
                                   FROM social_group_members m JOIN users u ON u.id = m.user_id'
                . $cond . " ORDER BY FIELD(m.role, 'owner', 'admin', 'member'), m.joined_at ASC, m.id ASC
                          LIMIT " . $ps . ' OFFSET ' . $off);
            $st->execute($args);
            $list = [];
            foreach ($st->fetchAll() as $u) {
                $item = user_brief($u);
                $item['role']        = (string)$u['group_role'];
                $item['group_role']  = (string)$u['group_role'];
                $item['joined_at']   = (string)($u['joined_at'] ?? '');
                $item['global_role'] = (string)($u['role'] ?? 'user');
                $item['is_admin']    = ((string)($u['role'] ?? '')) === 'admin' ? 1 : 0;
                $list[] = $item;
            }
            json_out(['list' => $list, 'page' => $page, 'page_size' => $ps, 'total' => $total]);

        case 'admin_group_member_remove':
            require_admin();
            $gid = (int)param('group_id', 0);
            $uid = (int)param('user_id', 0);
            if ($gid <= 0 || $uid <= 0) json_error('参数错误');
            social_group_or_404($gid, false);
            $del = db()->prepare('DELETE FROM social_group_members WHERE group_id = ? AND user_id = ?');
            $del->execute([$gid, $uid]);
            if ($del->rowCount() <= 0) json_error('成员不存在');
            json_out([
                'group_id'     => $gid,
                'user_id'      => $uid,
                'removed'      => 1,
                'member_count' => social_member_count($gid),
            ]);

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

        // ============ 后台: 抽奖管理 ============
        case 'admin_lottery_prizes':
            require_admin();
            $rows = db()->query("SELECT p.*,
                    (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id) AS total_cnt,
                    (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id AND c.user_id > 0) AS used_cnt
                FROM lottery_prizes p ORDER BY p.sort_order ASC, p.id ASC")->fetchAll();
            $list = [];
            foreach ($rows as $r) {
                $list[] = [
                    'id' => (int)$r['id'], 'name' => (string)$r['name'], 'card_type' => (string)$r['card_type'],
                    'description' => (string)($r['description'] ?? ''), 'sort_order' => (int)$r['sort_order'],
                    'weight' => (int)($r['weight'] ?? 0) > 0 ? (int)$r['weight'] : 100,
                    'is_active' => (int)$r['is_active'], 'total' => (int)$r['total_cnt'],
                    'used' => (int)$r['used_cnt'], 'left' => (int)$r['total_cnt'] - (int)$r['used_cnt'],
                    'created_at' => (string)$r['created_at'],
                ];
            }
            json_out(['list' => $list]);

        case 'admin_lottery_prize_save':
            require_admin();
            $id   = (int)param('id', 0);
            $name = trim((string)param('name', ''));
            $type = trim((string)param('card_type', '通用'));
            $desc = trim((string)param('description', ''));
            $sort = (int)param('sort_order', 0);
            $act  = (int)param('is_active', 1) === 1 ? 1 : 0;
            $weight = (int)param('weight', 100); // 后台 a-input-number 提交数字, 兼容 "100" 字符串
            if ($weight < 0) $weight = 0;
            if ($weight > 1000) json_error('奖项权重不能超过 1000');
            if ($name === '') json_error('奖项名称不能为空');
            if (mb_strlen($name) > 50) json_error('奖项名称不能超过 50 个字');
            if ($type === '') $type = '通用';
            if (mb_strlen($type) > 20) json_error('卡密类型不能超过 20 个字');
            if ($desc !== '' && mb_strlen($desc) > 255) json_error('描述不能超过 255 个字');
            $st = db()->prepare('SELECT id FROM lottery_prizes WHERE name = ?');
            $st->execute([$name]);
            $dup = (int)($st->fetchColumn() ?: 0);
            if ($dup > 0 && $dup !== $id) json_error('奖项名称已存在');
            if ($id > 0) {
                db()->prepare('UPDATE lottery_prizes SET name = ?, card_type = ?, description = ?, sort_order = ?, is_active = ?, weight = ? WHERE id = ?')
                    ->execute([$name, $type, $desc, $sort, $act, $weight, $id]);
                json_out(['id' => $id, 'weight' => $weight]);
            }
            db()->prepare('INSERT INTO lottery_prizes (name, card_type, description, sort_order, is_active, weight) VALUES (?, ?, ?, ?, ?, ?)')
                ->execute([$name, $type, $desc, $sort, $act, $weight]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'admin_lottery_prize_delete':
            require_admin();
            $id = (int)param('id', 0);
            if ($id <= 0) json_error('参数错误');
            $st = db()->prepare('SELECT COUNT(*) FROM lottery_draws WHERE prize_id = ?');
            $st->execute([$id]);
            if ((int)$st->fetchColumn() > 0) json_error('该奖项已有用户中奖, 不能删除');
            $st = db()->prepare('SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?');
            $st->execute([$id]);
            $n = (int)$st->fetchColumn();
            db()->prepare('DELETE FROM lottery_codes WHERE prize_id = ?')->execute([$id]);
            db()->prepare('DELETE FROM lottery_prizes WHERE id = ?')->execute([$id]);
            json_out(['deleted_codes' => $n]);

        case 'admin_lottery_codes':
            require_admin();
            $page = max(1, (int)param('page', 1));
            $ps = min(100, max(1, (int)param('page_size', 20)));
            $pid = (int)param('prize_id', 0);
            $status = (string)param('status', 'all');
            $kw = trim((string)param('keyword', ''));
            $where = []; $args = [];
            if ($pid > 0) { $where[] = 'c.prize_id = ?'; $args[] = $pid; }
            if ($status === 'unused') { $where[] = 'c.user_id = 0'; }
            elseif ($status === 'used') { $where[] = 'c.user_id > 0'; }
            if ($kw !== '') { $where[] = 'c.code LIKE ?'; $args[] = '%' . $kw . '%'; }
            $cond = $where ? ('WHERE ' . implode(' AND ', $where)) : '';
            $st = db()->prepare('SELECT COUNT(*) FROM lottery_codes c ' . $cond);
            $st->execute($args);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare("SELECT c.*, p.name AS prize_name, p.card_type, u.nickname, u.username
                FROM lottery_codes c
                LEFT JOIN lottery_prizes p ON p.id = c.prize_id
                LEFT JOIN users u ON u.id = c.user_id
                " . $cond . ' ORDER BY c.id DESC LIMIT ' . $ps . ' OFFSET ' . (($page - 1) * $ps));
            $st->execute($args);
            $list = [];
            foreach ($st->fetchAll() as $r) {
                $uid = (int)$r['user_id'];
                $nn = (string)($r['nickname'] ?? '');
                $list[] = [
                    'id' => (int)$r['id'], 'prize_id' => (int)$r['prize_id'],
                    'prize_name' => (string)($r['prize_name'] ?? ''), 'card_type' => (string)($r['card_type'] ?? ''),
                    'code' => (string)$r['code'], 'status' => $uid > 0 ? 'used' : 'unused',
                    'user_id' => $uid,
                    'user_nickname' => $uid > 0 ? ($nn !== '' ? $nn : (string)($r['username'] ?? '')) : '',
                    'used_at' => (string)($r['used_at'] ?? ''), 'created_at' => (string)$r['created_at'],
                ];
            }
            $unused = (int)db()->query('SELECT COUNT(*) FROM lottery_codes WHERE user_id = 0')->fetchColumn();
            $used = (int)db()->query('SELECT COUNT(*) FROM lottery_codes WHERE user_id > 0')->fetchColumn();
            json_out([
                'list' => $list, 'total' => $total, 'page' => $page, 'page_size' => $ps,
                'unused' => $unused, 'used' => $used,
            ]);

        case 'admin_lottery_codes_import':
            require_admin();
            $pid = (int)param('prize_id', 0);
            $text = (string)param('text', '');
            if ($pid <= 0) json_error('请选择奖项');
            $st = db()->prepare('SELECT id FROM lottery_prizes WHERE id = ?');
            $st->execute([$pid]);
            if (!$st->fetchColumn()) json_error('奖项不存在');
            $lines = preg_split('/\r\n|\r|\n/', $text);
            $added = 0; $dup = 0; $invalid = 0; $seen = [];
            $ins = db()->prepare('INSERT IGNORE INTO lottery_codes (prize_id, code) VALUES (?, ?)');
            foreach ($lines as $line) {
                $code = trim($line);
                if ($code === '') continue;
                if (!preg_match('/^[A-Za-z0-9_\-]{6,64}$/', $code)) { $invalid++; continue; }
                if (isset($seen[$code])) { $dup++; continue; }
                $seen[$code] = true;
                $ins->execute([$pid, $code]);
                if ($ins->rowCount() > 0) $added++; else $dup++;
            }
            $st = db()->prepare('SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ? AND user_id = 0');
            $st->execute([$pid]);
            json_out(['added' => $added, 'duplicate' => $dup, 'invalid' => $invalid, 'left' => (int)$st->fetchColumn()]);

        case 'admin_lottery_codes_delete':
            require_admin();
            $pid = (int)param('prize_id', 0);
            $status = (string)param('status', 'unused');
            // 清空未使用卡密时, prize_id = 0 表示「全部奖项」(后台在"全部奖项"筛选下会这样传)
            if ($pid <= 0 && $status === 'all') json_error('请选择奖项');
            if ($status === 'all') {
                // 连已抽出的记录一起清掉 (谨慎操作)
                $st = db()->prepare('SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?');
                $st->execute([$pid]);
                $n = (int)$st->fetchColumn();
                db()->prepare('DELETE FROM lottery_draws WHERE prize_id = ?')->execute([$pid]);
                db()->prepare('DELETE FROM lottery_codes WHERE prize_id = ?')->execute([$pid]);
                json_out(['deleted' => $n]);
            }
            if ($pid > 0) {
                $st = db()->prepare('DELETE FROM lottery_codes WHERE prize_id = ? AND user_id = 0');
                $st->execute([$pid]);
            } else {
                $st = db()->query('DELETE FROM lottery_codes WHERE user_id = 0');
            }
            json_out(['deleted' => $st->rowCount()]);

        case 'admin_lottery_config_get':
            require_admin();
            json_out(lottery_config());

        case 'admin_lottery_config_set':
            require_admin();
            $old = lottery_config();
            $cfg = $old;
            $title = trim((string)param('title', $old['title']));
            $content = (string)param('content', $old['content']);
            if (mb_strlen($title) > 50) json_error('抽奖标题不能超过 50 个字');
            if (mb_strlen($content) > 2000) json_error('抽奖内容不能超过 2000 个字');
            $limit = (int)param('per_user_limit', (int)$old['per_user_limit']);
            if ($limit < 0) $limit = 0;
            if ($limit > 9999) json_error('每人抽奖次数不能超过 9999');
            $daily = (int)param('daily_limit', (int)$old['daily_limit']);
            if ($daily < 0) $daily = 0;
            if ($daily > 999) json_error('每日抽奖次数不能超过 999');
            $week = (int)param('week_limit', (int)$old['week_limit']);
            if ($week < 0) $week = 0;
            if ($week > 999) json_error('每周抽奖次数不能超过 999');
            $cool = (int)param('cooldown_seconds', (int)$old['cooldown_seconds']);
            if ($cool < 0) $cool = 0;
            if ($cool > 86400) json_error('冷却时间不能超过 86400 秒');
            $dtl = (int)param('daily_total_limit', (int)$old['daily_total_limit']);
            if ($dtl < 0) $dtl = 0;
            if ($dtl > 999999) json_error('今天发放上限不能超过 999999');
            $cfg['enabled'] = lottery_flag(param('enabled', (int)$old['enabled']));
            $cfg['title'] = $title;
            $cfg['content'] = $content;
            $cfg['per_user_limit'] = $limit;
            $cfg['daily_limit'] = $daily;
            $cfg['week_limit'] = $week;
            $cfg['cooldown_seconds'] = $cool;
            $cfg['daily_total_limit'] = $dtl;
            $cfg['daily_reset_time'] = lottery_config_validate_time(param('daily_reset_time', $old['daily_reset_time']));
            $cfg['success_text'] = lottery_config_validate_text('提示文案', param('success_text', $old['success_text']), 200);
            $cfg['empty_text'] = lottery_config_validate_text('提示文案', param('empty_text', $old['empty_text']), 200);
            $cfg['show_prizes'] = lottery_flag(param('show_prizes', (int)$old['show_prizes']));
            $cfg['show_stock'] = lottery_flag(param('show_stock', (int)$old['show_stock']));
            $cfg['notify_winner'] = lottery_flag(param('notify_winner', (int)$old['notify_winner']));
            // windows_enabled=0 只让时段失效, 不动 windows 配置 (后台关开关后仍会原样提交 windows)
            $cfg['windows_enabled'] = lottery_flag(param('windows_enabled', (int)$old['windows_enabled']));
            $cfg['start_date'] = lottery_config_validate_date('活动开始日期', param('start_date', $old['start_date']));
            $cfg['end_date'] = lottery_config_validate_date('活动结束日期', param('end_date', $old['end_date']));
            if ($cfg['start_date'] !== '' && $cfg['end_date'] !== '' && $cfg['end_date'] < $cfg['start_date']) {
                json_error('结束日期不能早于开始日期');
            }
            $cfg['windows'] = lottery_config_validate_windows(param('windows', $old['windows']));
            lottery_config_save($cfg);
            json_out(lottery_config());

        case 'admin_lottery_window_preview':
            require_admin();
            $pcfg = lottery_config();
            $pwin = lottery_window_state($pcfg);
            $pwin['daily_total_left'] = lottery_daily_total_left($pcfg);
            $pwin['daily_total_limit'] = (int)$pcfg['daily_total_limit'];
            json_out($pwin);

        case 'admin_video_config_get':
            require_admin();
            // 顺便把占用统计带上, 后台卡片一次请求就能渲染
            $vout = video_config();
            $vout['usage'] = video_usage_stats();
            $vout['server_upload_max_mb'] = video_server_max_mb();
            json_out($vout);

        case 'admin_video_config_set':
            require_admin();
            $vcfg = video_config();
            $vmax = (int)param('max_mb', (int)$vcfg['max_mb']);
            if ($vmax < 1) json_error('单个视频上限不能小于 1MB');
            if ($vmax > 500) json_error('单个视频不能超过 500MB');
            $svrMax = video_server_max_mb();
            if ($svrMax > 0 && $vmax > $svrMax) {
                json_error('单个视频上限不能超过服务器上传上限 (' . $svrMax . 'MB), 请先调大 php.ini 的 upload_max_filesize');
            }
            $vtotal = (int)param('total_limit_mb', (int)$vcfg['total_limit_mb']);
            if ($vtotal < 100) json_error('总容量上限不能小于 100MB');
            if ($vtotal > 100000) json_error('总容量上限不能超过 100000MB');
            if ($vtotal < $vmax) json_error('总容量上限不能小于单个视频上限');
            $vdays = (int)param('keep_days', (int)$vcfg['keep_days']);
            if ($vdays < 0) json_error('保留天数不能为负数');
            if ($vdays > 3650) json_error('保留天数不能超过 3650 天');
            $vnew = [
                'enabled'        => video_flag(param('enabled', (int)$vcfg['enabled'])),
                'max_mb'         => $vmax,
                'total_limit_mb' => $vtotal,
                'keep_days'      => $vdays,
                'auto_clean'     => video_flag(param('auto_clean', (int)$vcfg['auto_clean'])),
            ];
            video_config_save($vnew);
            $vnew['usage'] = video_usage_stats();
            json_out($vnew);

        case 'admin_video_usage':
            require_admin();
            $vuse = video_usage_stats();
            $vuse['total_mb'] = round($vuse['total_bytes'] / 1048576, 2);
            $vuse['max_mb']   = round($vuse['max_size'] / 1048576, 2);
            $vuse['config']   = video_config();
            // 后台卡片用: 服务器自己允许的上传上限 (可能小于配置里的 max_mb, 那样配了也传不上去)
            $vuse['server_upload_max_mb'] = video_server_max_mb();
            json_out($vuse);

        case 'admin_video_clean':
            require_admin();
            // mode=over_limit: 只按总容量裁掉最旧的; mode=all: 清空全部视频
            // 手动清理是管理员的明确动作, 不受 auto_clean=0 限制
            $vmode = (string)param('mode', 'over_limit');
            if ($vmode !== 'over_limit' && $vmode !== 'all') json_error('清理模式不合法');
            $vres = ($vmode === 'all') ? video_clean_all() : video_cleanup(true, false, false);
            $vres['mode'] = $vmode;
            $vres['freed_mb'] = round($vres['freed_bytes'] / 1048576, 2);
            $vres['usage'] = video_usage_stats();
            json_out($vres);

        case 'admin_lottery_draws':
            require_admin();
            $page = max(1, (int)param('page', 1));
            $ps = min(100, max(1, (int)param('page_size', 20)));
            $pid = (int)param('prize_id', 0);
            $kw = trim((string)param('keyword', ''));
            $where = []; $args = [];
            if ($pid > 0) { $where[] = 'd.prize_id = ?'; $args[] = $pid; }
            if ($kw !== '') {
                $where[] = '(d.code LIKE ? OR u.username LIKE ? OR u.nickname LIKE ?)';
                $like = '%' . $kw . '%';
                array_push($args, $like, $like, $like);
            }
            $cond = $where ? ('WHERE ' . implode(' AND ', $where)) : '';
            $st = db()->prepare('SELECT COUNT(*) FROM lottery_draws d LEFT JOIN users u ON u.id = d.user_id ' . $cond);
            $st->execute($args);
            $total = (int)$st->fetchColumn();
            $st = db()->prepare('SELECT d.*, u.nickname, u.username, p.name AS prize_name, p.card_type
                FROM lottery_draws d LEFT JOIN users u ON u.id = d.user_id
                LEFT JOIN lottery_prizes p ON p.id = d.prize_id
                ' . $cond . ' ORDER BY d.id DESC LIMIT ' . $ps . ' OFFSET ' . (($page - 1) * $ps));
            $st->execute($args);
            $list = [];
            foreach ($st->fetchAll() as $r) {
                $nn = (string)($r['nickname'] ?? '');
                $list[] = [
                    'id' => (int)$r['id'], 'user_id' => (int)$r['user_id'],
                    'nickname' => $nn !== '' ? $nn : (string)($r['username'] ?? ''),
                    'username' => (string)($r['username'] ?? ''),
                    'prize_name' => (string)($r['prize_name'] ?? ''), 'card_type' => (string)($r['card_type'] ?? ''),
                    'code' => (string)$r['code'], 'created_at' => (string)$r['created_at'],
                ];
            }
            json_out(['list' => $list, 'total' => $total, 'page' => $page, 'page_size' => $ps]);

        case 'admin_lottery_quota_set':
            require_admin();
            $uid = (int)param('user_id', 0);
            $quota = (int)param('quota', -1);
            if ($uid <= 0) json_error('参数错误');
            if ($quota < -1) $quota = -1;
            if ($quota > 9999) json_error('抽奖次数不能超过 9999');
            $st = db()->prepare('SELECT * FROM users WHERE id = ?');
            $st->execute([$uid]);
            $u = $st->fetch();
            if (!$u) json_error('用户不存在');
            db()->prepare('UPDATE users SET lottery_quota = ? WHERE id = ?')->execute([$quota, $uid]);
            $u['lottery_quota'] = $quota;
            json_out(['user_id' => $uid, 'quota' => $quota, 'left' => lottery_left_for($u)]);

        // 重置单个用户的抽奖机会: 已抽次数清零(中奖记录保留), 次数立刻恢复
        case 'admin_lottery_quota_reset':
            require_admin();
            $uid = (int)param('user_id', 0);
            if ($uid <= 0) json_error('参数错误');
            $st = db()->prepare('SELECT * FROM users WHERE id = ?');
            $st->execute([$uid]);
            $u = $st->fetch();
            if (!$u) json_error('用户不存在');
            $daily = (int)param('daily', 0) === 1;
            // 统一用数据库的 NOW() 作为重置标记, 避免 PHP 与 MySQL 时区/秒差不一致
            $nowDb = (string)db()->query('SELECT NOW()')->fetchColumn();
            db()->prepare('UPDATE users SET lottery_reset_at = ?' . ($daily ? ', lottery_day_reset_at = ?' : '') . ' WHERE id = ?')
                ->execute($daily ? [$nowDb, $nowDb, $uid] : [$nowDb, $uid]);
            $u['lottery_reset_at'] = $nowDb;
            if ($daily) $u['lottery_day_reset_at'] = $nowDb;
            json_out([
                'user_id'     => $uid,
                'drawn'       => lottery_drawn_count($uid),
                'today_drawn' => lottery_drawn_count_today($uid),
                'left'        => lottery_left_for($u),
            ]);

        // 一键重置所有人的抽奖机会
        case 'admin_lottery_quota_reset_all':
            require_admin();
            $daily = (int)param('daily', 0) === 1;
            $nowDb = (string)db()->query('SELECT NOW()')->fetchColumn();
            $st = db()->prepare('UPDATE users SET lottery_reset_at = ?' . ($daily ? ', lottery_day_reset_at = ?' : ''));
            $st->execute($daily ? [$nowDb, $nowDb] : [$nowDb]);
            json_out(['updated' => $st->rowCount(), 'daily' => $daily ? 1 : 0]);

        /**
         * 抽奖活动重置。
         * mode = quota    只清所有人「总抽奖次数」(中奖记录与已用卡密都保留)
         * mode = daily    只清所有人「今日已抽次数」(总次数不变, 今天能再抽)
         * mode = activity 整个活动重开: 删中奖记录 + 所有人总次数与今日次数归零
         *                 + release_codes=1 把已经发出去的卡密退回卡密池(user_id=0)
         *                 + clear_notices=1 删掉含卡密的通知, 避免旧卡密还能被看到
         */
        case 'admin_lottery_activity_reset':
            require_admin();
            $mode = trim((string)param('mode', 'quota'));
            if (!in_array($mode, ['quota', 'daily', 'activity'], true)) json_error('参数错误');
            $release = (int)param('release_codes', 1) === 1;
            $clearNotices = (int)param('clear_notices', 1) === 1;
            $now = (string)db()->query('SELECT NOW()')->fetchColumn();
            $res = [
                'mode' => $mode, 'users' => 0, 'draws_deleted' => 0,
                'codes_released' => 0, 'notices_deleted' => 0,
            ];
            if ($mode === 'daily') {
                $st = db()->prepare('UPDATE users SET lottery_day_reset_at = ?');
                $st->execute([$now]);
                $res['users'] = (int)$st->rowCount();
                json_out($res);
            }
            $st = db()->prepare('UPDATE users SET lottery_reset_at = ?' . ($mode === 'activity' ? ', lottery_day_reset_at = ?' : ''));
            $st->execute($mode === 'activity' ? [$now, $now] : [$now]);
            $res['users'] = (int)$st->rowCount();
            if ($mode === 'activity') {
                $res['draws_deleted'] = (int)db()->exec('DELETE FROM lottery_draws');
                if ($release) {
                    $res['codes_released'] = (int)db()->exec('UPDATE lottery_codes SET user_id = 0, used_at = NULL WHERE user_id <> 0');
                }
                if ($clearNotices) {
                    $res['notices_deleted'] = (int)db()->exec("DELETE FROM notifications WHERE type = 'lottery'");
                }
            }
            json_out($res);

        case 'admin_lottery_quota_all':
            require_admin();
            $quota = (int)param('quota', -1);
            if ($quota < -1) $quota = -1;
            if ($quota > 9999) json_error('抽奖次数不能超过 9999');
            $st = db()->prepare('UPDATE users SET lottery_quota = ?');
            $st->execute([$quota]);
            json_out(['updated' => $st->rowCount()]);

        // ============ 后台: 私聊管理 ============
        case 'admin_pm_conversations':
            require_admin();
            $page = max(1, (int)param('page', 1));
            $ps = min(100, max(1, (int)param('page_size', 20)));
            $kw = trim((string)param('keyword', ''));
            $where = []; $args = [];
            if ($kw !== '') {
                $where[] = '(ua.nickname LIKE ? OR ua.username LIKE ? OR ub.nickname LIKE ? OR ub.username LIKE ?)';
                $like = '%' . $kw . '%';
                array_push($args, $like, $like, $like, $like);
            }
            $wc = $where ? (' WHERE ' . implode(' AND ', $where)) : '';
            $cnt = db()->prepare('SELECT COUNT(*) FROM social_pms c
                INNER JOIN users ua ON ua.id = c.user_a INNER JOIN users ub ON ub.id = c.user_b' . $wc);
            $cnt->execute($args);
            $total = (int)$cnt->fetchColumn();
            $off = ($page - 1) * $ps;
            $st = db()->prepare('SELECT c.id, c.last_message_id, c.last_at, c.created_at,
                    ua.id AS a_id, ua.username AS a_username, ua.nickname AS a_nickname, ua.avatar AS a_avatar,
                    ub.id AS b_id, ub.username AS b_username, ub.nickname AS b_nickname, ub.avatar AS b_avatar,
                    (SELECT COUNT(*) FROM social_pm_messages m WHERE m.conv_id = c.id) AS message_count,
                    (SELECT m2.content FROM social_pm_messages m2 WHERE m2.conv_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_content,
                    (SELECT m3.image FROM social_pm_messages m3 WHERE m3.conv_id = c.id ORDER BY m3.id DESC LIMIT 1) AS last_image
                FROM social_pms c
                INNER JOIN users ua ON ua.id = c.user_a INNER JOIN users ub ON ub.id = c.user_b'
                . $wc . ' ORDER BY c.last_message_id DESC, c.id DESC LIMIT ' . $off . ', ' . $ps);
            $st->execute($args);
            $list = [];
            foreach ($st->fetchAll() as $c) {
                $list[] = [
                    'id'             => (int)$c['id'],
                    'user_a'         => user_brief(['id' => $c['a_id'], 'username' => $c['a_username'], 'nickname' => $c['a_nickname'], 'avatar' => $c['a_avatar']]),
                    'user_b'         => user_brief(['id' => $c['b_id'], 'username' => $c['b_username'], 'nickname' => $c['b_nickname'], 'avatar' => $c['b_avatar']]),
                    'message_count'  => (int)$c['message_count'],
                    'last_content'   => (string)($c['last_content'] ?? ''),
                    'last_image'     => (string)($c['last_image'] ?? ''),
                    'last_at'        => (string)($c['last_at'] ?? ''),
                    'created_at'     => (string)$c['created_at'],
                ];
            }
            json_out(['list' => $list, 'total' => $total, 'page' => $page, 'page_size' => $ps]);

        case 'admin_pm_messages':
            require_admin();
            $cid = (int)param('conv_id', 0);
            if ($cid <= 0) json_error('参数错误');
            $st = db()->prepare('SELECT * FROM social_pms WHERE id = ?');
            $st->execute([$cid]);
            if (!$st->fetch()) json_error('会话不存在');
            $page = max(1, (int)param('page', 1));
            $ps = min(100, max(1, (int)param('page_size', 20)));
            $kw = trim((string)param('keyword', ''));
            $where = ['m.conv_id = ?']; $args = [$cid];
            if ($kw !== '') { $where[] = 'm.content LIKE ?'; $args[] = '%' . $kw . '%'; }
            $wc = ' WHERE ' . implode(' AND ', $where);
            $cnt = db()->prepare('SELECT COUNT(*) FROM social_pm_messages m' . $wc);
            $cnt->execute($args);
            $total = (int)$cnt->fetchColumn();
            $off = ($page - 1) * $ps;
            $st = db()->prepare('SELECT m.*, uf.nickname AS from_nickname, uf.username AS from_username,
                    ut.nickname AS to_nickname, ut.username AS to_username
                FROM social_pm_messages m
                LEFT JOIN users uf ON uf.id = m.from_user
                LEFT JOIN users ut ON ut.id = m.to_user'
                . $wc . ' ORDER BY m.id DESC LIMIT ' . $off . ', ' . $ps);
            $st->execute($args);
            $list = [];
            foreach ($st->fetchAll() as $m) {
                $list[] = [
                    'id'          => (int)$m['id'],
                    'conv_id'     => (int)$m['conv_id'],
                    'from_user'   => (int)$m['from_user'],
                    'to_user'     => (int)$m['to_user'],
                    'from_name'   => (string)(((string)($m['from_nickname'] ?? '')) !== '' ? $m['from_nickname'] : ($m['from_username'] ?? '')),
                    'to_name'     => (string)(((string)($m['to_nickname'] ?? '')) !== '' ? $m['to_nickname'] : ($m['to_username'] ?? '')),
                    'content'     => (string)($m['content'] ?? ''),
                    'image'       => (string)($m['image'] ?? ''),
                    'msg_type'    => ((string)($m['video'] ?? '') !== '' ? 'video' : (string)($m['msg_type'] ?? '')),
                    'video'       => (string)($m['video'] ?? ''),
                    'is_recalled' => (int)$m['is_recalled'],
                    'created_at'  => (string)$m['created_at'],
                ];
            }
            json_out(['list' => $list, 'total' => $total, 'page' => $page, 'page_size' => $ps]);

        case 'admin_pm_message_delete':
            require_admin();
            $id = (int)param('id', 0);
            if ($id <= 0) json_error('参数错误');
            $st = db()->prepare('SELECT * FROM social_pm_messages WHERE id = ?');
            $st->execute([$id]);
            $m = $st->fetch();
            if (!$m) json_error('消息不存在');
            db()->prepare('DELETE FROM social_pm_messages WHERE id = ?')->execute([$id]);
            $cid = (int)$m['conv_id'];
            $st = db()->prepare('SELECT COALESCE(MAX(id), 0) FROM social_pm_messages WHERE conv_id = ?');
            $st->execute([$cid]);
            $newLast = (int)$st->fetchColumn();
            $last = '';
            if ($newLast > 0) {
                $st = db()->prepare('SELECT created_at FROM social_pm_messages WHERE id = ?');
                $st->execute([$newLast]);
                $last = (string)($st->fetchColumn() ?: '');
            }
            db()->prepare('UPDATE social_pms SET last_message_id = ?, last_at = ? WHERE id = ?')
                ->execute([$newLast, ($last !== '' ? $last : null), $cid]);
            json_out(['ok' => true, 'deleted' => 1]);

        case 'admin_pm_clear':
            require_admin();
            $cid = (int)param('conv_id', 0);
            if ($cid <= 0) json_error('请选择会话');
            $st = db()->prepare('SELECT COUNT(*) FROM social_pm_messages WHERE conv_id = ?');
            $st->execute([$cid]);
            $n = (int)$st->fetchColumn();
            db()->prepare('DELETE FROM social_pm_messages WHERE conv_id = ?')->execute([$cid]);
            db()->prepare('UPDATE social_pms SET last_message_id = 0, last_at = NULL WHERE id = ?')->execute([$cid]);
            json_out(['deleted' => $n]);

        // ============ 后台: 用户标签 ============
        case 'admin_user_tags_set':
            require_admin();
            $uid = (int)param('user_id', 0);
            if ($uid <= 0) json_error('参数错误');
            $str = user_tags_str((string)param('tags', ''));
            $st = db()->prepare('SELECT id FROM users WHERE id = ?');
            $st->execute([$uid]);
            if (!$st->fetch()) json_error('用户不存在');
            db()->prepare('UPDATE users SET tags = ? WHERE id = ?')->execute([$str, $uid]);
            json_out(['user_id' => $uid, 'tags' => $str === '' ? [] : explode(',', $str)]);

        case 'admin_tags':
            require_admin();
            $presets = json_decode(setting_get('user_tag_presets', '[]'), true);
            if (!is_array($presets)) $presets = [];
            $used = [];
            foreach (db()->query('SELECT tags FROM users WHERE tags <> \'\'')->fetchAll() as $r) {
                foreach (user_tags_arr(['tags' => $r['tags']]) as $t) {
                    if (!in_array($t, $used, true)) $used[] = $t;
                }
            }
            json_out(['presets' => array_values($presets), 'used' => $used]);

        case 'admin_tag_preset_save':
            require_admin();
            $tag = trim((string)param('tag', ''));
            if ($tag === '') json_error('标签不能为空');
            if (mb_strlen($tag) > 10) json_error('标签不能超过 10 个字');
            $presets = json_decode(setting_get('user_tag_presets', '[]'), true);
            if (!is_array($presets)) $presets = [];
            if (!in_array($tag, $presets, true)) {
                if (count($presets) >= 30) json_error('预设标签最多 30 个');
                $presets[] = $tag;
            }
            setting_set('user_tag_presets', json_encode(array_values($presets), JSON_UNESCAPED_UNICODE));
            json_out(['presets' => array_values($presets)]);

        case 'admin_tag_preset_delete':
            require_admin();
            $tag = trim((string)param('tag', ''));
            $presets = json_decode(setting_get('user_tag_presets', '[]'), true);
            if (!is_array($presets)) $presets = [];
            $presets = array_values(array_filter($presets, function ($t) use ($tag) { return (string)$t !== $tag; }));
            setting_set('user_tag_presets', json_encode($presets, JSON_UNESCAPED_UNICODE));
            json_out(['presets' => $presets]);

        // ============ 后台: 群消息一键清除 ============
        case 'admin_social_message_clear':
            require_admin();
            $gid = (int)param('group_id', 0);
            $kw = trim((string)param('keyword', ''));
            $where = []; $args = [];
            if ($gid > 0) { $where[] = 'group_id = ?'; $args[] = $gid; }
            if ($kw !== '') { $where[] = 'content LIKE ?'; $args[] = '%' . $kw . '%'; }
            $cond = $where ? ('WHERE ' . implode(' AND ', $where)) : '';
            $st = db()->prepare('SELECT COUNT(*) FROM social_messages ' . $cond);
            $st->execute($args);
            $n = (int)$st->fetchColumn();
            if ($n > 0) { db()->prepare('DELETE FROM social_messages ' . $cond)->execute($args); }
            json_out(['deleted' => $n]);

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
        'tags'           => user_tags_arr($u),
    ];
}


/**
 * 从 QQ 邮箱推导 QQ 头像地址 (腾讯公开的 qlogo 接口)。
 * 例: 12345678@qq.com -> https://q1.qlogo.cn/g?b=qq&nk=12345678&s=640
 * 非 QQ 邮箱返回空串 (不做兜底, 让前端显示昵称首字)。
 */
function qq_avatar_from_email(string $email): string
{
    $email = trim($email);
    if (preg_match('/^(\d{5,12})@(qq\.com|vip\.qq\.com)$/i', $email, $m)) {
        return 'https://q1.qlogo.cn/g?b=qq&nk=' . $m[1] . '&s=640';
    }
    return '';
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
/** 群成员真实人数 (social_group_members 计数; 单请求内缓存) */
function social_member_count(int $gid): int
{
    static $cache = [];
    if (array_key_exists($gid, $cache)) return $cache[$gid];
    try {
        $st = db()->prepare('SELECT COUNT(*) FROM social_group_members WHERE group_id = ?');
        $st->execute([$gid]);
        $cache[$gid] = (int)$st->fetchColumn();
    } catch (Exception $e) {
        $cache[$gid] = 0;   // 迁移未跑时不能让接口 500
    }
    return $cache[$gid];
}

/** 是否群成员 (游客 = 0; $uid <= 0 时取当前登录用户) */
function social_is_member(int $gid, int $uid = 0): int
{
    if ($uid <= 0) {
        $u = current_user();
        if (!$u) return 0;
        $uid = (int)$u['id'];
    }
    static $cache = [];
    $key = $gid . ':' . $uid;
    if (array_key_exists($key, $cache)) return $cache[$key];
    try {
        $st = db()->prepare('SELECT 1 FROM social_group_members WHERE group_id = ? AND user_id = ? LIMIT 1');
        $st->execute([$gid, $uid]);
        $cache[$key] = $st->fetchColumn() ? 1 : 0;
    } catch (Exception $e) {
        $cache[$key] = 0;
    }
    return $cache[$key];
}

/** 昵称 (空则用户名) —— 系统消息文案里用 */
function social_user_display_name(array $u): string
{
    $nick = trim((string)($u['nickname'] ?? ''));
    if ($nick !== '') return $nick;
    $name = (string)($u['username'] ?? '');
    return $name !== '' ? $name : ('用户' . (int)($u['id'] ?? 0));
}

/** 写一条系统消息: msg_type='system', at_users = NULL (不进未读计数) */
function social_system_message(int $gid, int $uid, string $content): int
{
    db()->prepare("INSERT INTO social_messages
            (group_id, user_id, content, image, image_w, image_h, at_users, quote_id, is_recalled, msg_type)
            VALUES (?, ?, ?, '', 0, 0, NULL, 0, 0, 'system')")
        ->execute([$gid, $uid, $content]);
    return (int)db()->lastInsertId();
}

function social_group_public(array $g, int $muted = 0, int $unread = 0, int $firstUnreadId = 0, $last = null, array $mention = []): array
{
    $lastArr = null;
    if (is_array($last) && $last) {
        $lastArr = [
            'id'         => (int)$last['id'],
            'user_id'    => (int)$last['user_id'],
            'nickname'   => (string)(((string)($last['nickname'] ?? '')) !== '' ? $last['nickname'] : ($last['username'] ?? '')),
            'content'    => (string)($last['content'] ?? ''),
            'image'      => (string)($last['image'] ?? ''),
            'msg_type'   => (string)($last['msg_type'] ?? ''),
            'video'      => (string)($last['video'] ?? ''),
            'video_w'    => (int)($last['video_w'] ?? 0),
            'video_h'    => (int)($last['video_h'] ?? 0),
            'video_duration' => (int)($last['video_duration'] ?? 0),
            'created_at' => (string)($last['created_at'] ?? ''),
        ];
    }
    return [
        'muted'         => $muted,
        'unread'        => $unread,
        'first_unread_id' => $firstUnreadId,
        'last_message'  => $lastArr,
        'last_time'     => $lastArr ? (string)$lastArr['created_at'] : '',
        'at_me'         => (int)($mention['at_me'] ?? 0),
        'at_me_first'   => (int)($mention['at_me_first'] ?? 0),
        'at_all'        => (int)($mention['at_all'] ?? 0),
        'at_all_first'  => (int)($mention['at_all_first'] ?? 0),
        'id'            => (int)$g['id'],
        'name'          => (string)$g['name'],
        'icon'          => (string)($g['icon'] ?? ''),
        'description'   => (string)($g['description'] ?? ''),
        'notice'        => (string)($g['notice'] ?? ''),
        'member_count'  => social_member_count((int)$g['id']),
        'is_member'     => social_is_member((int)$g['id']),
        'message_count' => (int)($g['message_count'] ?? 0),
        'sort_order'    => (int)($g['sort_order'] ?? 0),
        'is_active'     => (int)($g['is_active'] ?? 1),
        'all_muted'     => (int)($g['all_muted'] ?? 0),
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
        'tags'        => user_tags_arr($m),
        'msg_type'    => (string)($m['msg_type'] ?? ''),
        'content'     => $recalled ? '' : (string)$m['content'],
        'image'       => $recalled ? '' : (string)($m['image'] ?? ''),
        'image_w'     => $recalled ? 0 : (int)($m['image_w'] ?? 0),
        'image_h'     => $recalled ? 0 : (int)($m['image_h'] ?? 0),
        'video'          => $recalled ? '' : (string)($m['video'] ?? ''),
        'video_w'        => $recalled ? 0 : (int)($m['video_w'] ?? 0),
        'video_h'        => $recalled ? 0 : (int)($m['video_h'] ?? 0),
        'video_duration' => $recalled ? 0 : (int)($m['video_duration'] ?? 0),
        'video_size'     => $recalled ? 0 : (int)($m['video_size'] ?? 0),
        'at'          => $at,
        'quote_id'      => (int)($m['quote_id'] ?? 0),
        'quote_nickname' => $recalled ? '' : (string)($m['quote_nickname'] ?? ''),
        'quote_content'  => $recalled ? '' : (string)($m['quote_content'] ?? ''),
        'is_recalled' => $recalled ? 1 : 0,
        'created_at'  => (string)$m['created_at'],
        'time_text'   => date('H:i', $ts ?: time()),
    ];
}

/**
 * SSE 实时消息推送主循环 (契约 A2/A3/A4)。
 * 只在 case 'stream' 中调用; 直接输出事件流, 正常不会返回 (超时/断开都走 exit)。
 *
 * @param array $me     当前登录用户行 (current_user() 的结果)
 * @param int   $pmCur  客户端已见过的最大私聊消息 id (<=0 = 从现在开始)
 * @param int   $grpCur 客户端已见过的最大群消息 id (<=0 = 从现在开始)
 */
function sse_run(array $me, int $pmCur, int $grpCur): void
{
    $meId = (int)$me['id'];

    // 游标 <=0 => 「从现在开始」: 建立连接时初始化为当前 MAX(id), 绝不重放历史 (契约 A1)
    if ($pmCur <= 0) {
        $st = db()->prepare('SELECT COALESCE(MAX(id), 0) FROM social_pm_messages');
        $st->execute();
        $pmCur = (int)$st->fetchColumn();
    }
    if ($grpCur <= 0) {
        $st = db()->prepare('SELECT COALESCE(MAX(id), 0) FROM social_messages');
        $st->execute();
        $grpCur = (int)$st->fetchColumn();
    }

    // 长连接: 关掉全部输出缓冲与 gzip, 不设脚本超时 (契约 A2)
    while (ob_get_level() > 0) { ob_end_flush(); }
    @ini_set('zlib.output_compression', '0');
    @ini_set('output_buffering', '0');
    set_time_limit(0);
    ignore_user_abort(true);

    header('Content-Type: text/event-stream; charset=utf-8');
    header('Cache-Control: no-cache, no-store, must-revalidate');
    header('X-Accel-Buffering: no');
    header('Connection: keep-alive');
    echo "retry: 2000\n\n";
    flush();

    $startAt  = time();
    $lastHbAt = $startAt;
    $maxSec   = 25;   // 单连接封顶 25 秒, 到点发 bye 让客户端立刻重连, 不长期占用 worker
    $hbSec    = 10;   // 心跳间隔
    $mutedIds = my_muted_ids();   // 收件人对各群的免打扰状态 (key = group_id), 心跳时刷新

    while (true) {
        if (connection_aborted()) exit;              // 客户端断开 -> 立刻退出
        $now = time();
        if ($now - $startAt >= $maxSec) {            // 25 秒封顶: event: bye
            sse_event('bye', ['reason' => 'timeout']);
            exit;
        }
        if ($now - $lastHbAt >= $hbSec) {            // 心跳: 注释行, 客户端忽略
            echo ": hb\n\n";
            flush();
            $mutedIds = my_muted_ids();   // 免打扰状态变化后 10 秒内生效
            $lastHbAt = $now;
        }

        // ---- 私聊: 别人发给我的、未撤回的 (契约 A4, 只用参数绑定) ----
        // 视频列先探测再拼 SELECT: 迁移没跑时自动省略, SSE 不会因为新列挂掉
        $vselPm = db_has_column('social_pm_messages', 'video')
            ? ', m.video, m.video_w, m.video_h, m.video_duration, m.video_size' : '';
        $st = db()->prepare('SELECT m.id, m.conv_id, m.from_user, m.content, m.image' . $vselPm . ', m.created_at,
                                    u.nickname, u.username
                               FROM social_pm_messages m JOIN users u ON u.id = m.from_user
                              WHERE m.to_user = :me AND m.id > :cur AND m.is_recalled = 0
                              ORDER BY m.id ASC LIMIT 20');
        $st->execute([':me' => $meId, ':cur' => $pmCur]);
        foreach ($st->fetchAll() as $r) {
            if ((int)$r['id'] > $pmCur) $pmCur = (int)$r['id'];
            if ((int)$r['from_user'] === $meId) continue;   // 自己发的不推
            sse_event('pm', [
                'id'         => (int)$r['id'],
                'conv_id'    => (int)$r['conv_id'],
                'from_user'  => (int)$r['from_user'],
                'nickname'   => (string)(($r['nickname'] ?? '') !== '' ? $r['nickname'] : ('用户' . (int)$r['from_user'])),
                'username'   => (string)($r['username'] ?? ''),
                'content'    => (string)($r['content'] ?? ''),
                'image'      => (string)($r['image'] ?? ''),
                'msg_type'   => (($r['video'] ?? '') !== '' ? 'video' : ''),
                'video'          => (string)($r['video'] ?? ''),
                'video_w'        => (int)($r['video_w'] ?? 0),
                'video_h'        => (int)($r['video_h'] ?? 0),
                'video_duration' => (int)($r['video_duration'] ?? 0),
                'video_size'     => (int)($r['video_size'] ?? 0),
                'created_at' => (string)($r['created_at'] ?? ''),
            ]);
        }

        // ---- 群消息: 我以外的人发的、未撤回的 (契约 A4) ----
        $vselGrp = db_has_column('social_messages', 'video')
            ? ', m.video, m.video_w, m.video_h, m.video_duration, m.video_size' : '';
        $st = db()->prepare('SELECT m.id, m.group_id, m.user_id, m.content, m.image, m.at_users, m.msg_type' . $vselGrp . ', m.created_at,
                                    u.nickname, g.name AS group_name
                               FROM social_messages m
                               JOIN users u ON u.id = m.user_id
                               LEFT JOIN social_groups g ON g.id = m.group_id
                              WHERE m.id > :cur AND m.user_id <> :me AND m.is_recalled = 0
                              ORDER BY m.id ASC LIMIT 30');
        $st->execute([':cur' => $grpCur, ':me' => $meId]);
        foreach ($st->fetchAll() as $r) {
            if ((int)$r['id'] > $grpCur) $grpCur = (int)$r['id'];
            $atMe  = 0;
            $atAll = 0;
            $rawAt = (string)($r['at_users'] ?? '');
            if ($rawAt !== '') {
                foreach (explode(',', $rawAt) as $v) {
                    $v = (int)trim($v);
                    if ($v === 0) $atAll = 1;                 // 0 = 所有人 (真实用户 id 都 > 0)
                    if ($v > 0 && $v === $meId) $atMe = 1;
                }
            }
            sse_event('group', [
                'id'         => (int)$r['id'],
                'group_id'   => (int)$r['group_id'],
                'group_name' => (string)($r['group_name'] ?? ''),
                'user_id'    => (int)$r['user_id'],
                'nickname'   => (string)(($r['nickname'] ?? '') !== '' ? $r['nickname'] : ('用户' . (int)$r['user_id'])),
                'content'    => (string)($r['content'] ?? ''),
                'image'      => (string)($r['image'] ?? ''),
                'video'          => (string)($r['video'] ?? ''),
                'video_w'        => (int)($r['video_w'] ?? 0),
                'video_h'        => (int)($r['video_h'] ?? 0),
                'video_duration' => (int)($r['video_duration'] ?? 0),
                'video_size'     => (int)($r['video_size'] ?? 0),
                'at_me'      => $atMe,
                'at_all'     => $atAll,
                'muted'      => isset($mutedIds[(int)$r['group_id']]) ? 1 : 0,
                'msg_type'   => (string)($r['msg_type'] ?? ''),

                'created_at' => (string)($r['created_at'] ?? ''),
            ]);
        }

        usleep(1000000);   // 每 1.0 秒一个 tick
    }
}

/** 输出一条标准 SSE 事件 (event / data / 空行, 三行) */
function sse_event(string $event, array $data): void
{
    echo 'event: ' . $event . "\n";
    echo 'data: ' . json_encode($data, JSON_UNESCAPED_UNICODE) . "\n\n";
    flush();
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

/** 当前登录用户开了免打扰的群 id 集合 (key = group_id) */
/**
 * 用户在某群是否被管理员禁言 (过期自动清理)
 * group_id = 0 表示全站禁言
 */
function user_mute_state(int $uid, int $gid): ?array {
    $st = db()->prepare('SELECT id, group_id, until_at, reason FROM social_user_mutes
                         WHERE user_id = ? AND (group_id = ? OR group_id = 0)
                         ORDER BY group_id DESC LIMIT 1');
    $st->execute([$uid, $gid]);
    $row = $st->fetch();
    if (!$row) return null;
    if (!empty($row['until_at']) && strtotime($row['until_at']) <= time()) {
        db()->prepare('DELETE FROM social_user_mutes WHERE id = ?')->execute([(int)$row['id']]);
        return null;
    }
    return $row;
}

/** 禁言剩余时长文案: 永久 / 剩余 x 天|小时|分钟 */
/** 用户标签: users.tags 存逗号分隔, 最多 5 个, 每个不超过 10 个字 */
function user_tags_arr($u): array
{
    if (!is_array($u)) return [];
    $raw = trim((string)($u['tags'] ?? ''));
    if ($raw === '') return [];
    $out = [];
    foreach (explode(',', $raw) as $t) {
        $t = trim($t);
        if ($t === '' || mb_strlen($t) > 10) continue;
        if (!in_array($t, $out, true)) $out[] = $t;
        if (count($out) >= 5) break;
    }
    return $out;
}

/** 归一化后台传来的标签串 (支持逗号/顿号/空格分隔), 返回逗号分隔串 */
function user_tags_str(string $raw): string
{
    $parts = preg_split('/[,，、]+/u', $raw) ?: [];
    $out = [];
    foreach ($parts as $t) {
        $t = trim((string)$t);
        if ($t === '' || mb_strlen($t) > 10) continue;
        if (!in_array($t, $out, true)) $out[] = $t;
        if (count($out) >= 5) break;
    }
    return implode(',', $out);
}

// ==================== 视频消息 (契约 Wave 2 / 服务端) ====================

/** 视频配置默认值 (存在 settings.video_config) */
/** 表里有没有这一列 (静态缓存, 每次请求最多查一次): 迁移未跑时让新代码自动退回旧行为 */
function db_has_column(string $table, string $column): bool
{
    static $cache = [];
    $k = $table . '.' . $column;
    if (!array_key_exists($k, $cache)) {
        $cache[$k] = false;
        try {
            $st = db()->query('SHOW COLUMNS FROM `' . $table . '` LIKE ' . db()->quote($column));
            $cache[$k] = (bool)$st->fetch();
        } catch (Exception $e) { error_log('[db_has_column] ' . $e->getMessage()); }
    }
    return $cache[$k];
}

/** php.ini 里的 128M / 1G 这类值转成字节数 (0 = 不限或无法识别) */
function ini_bytes(string $val): int
{
    $val = trim($val);
    if ($val === '' || $val === '-1') return 0;
    $unit = strtolower(substr($val, -1));
    $num = (float)$val;
    if ($unit === 'g') return (int)round($num * 1073741824);
    if ($unit === 'm') return (int)round($num * 1048576);
    if ($unit === 'k') return (int)round($num * 1024);
    return (int)$num;
}

/** 服务器实际允许的上传上限 (MB): upload_max_filesize 与 post_max_size 取小的那个, 0 = 不限 */
function video_server_max_mb(): int
{
    $a = ini_bytes((string)ini_get('upload_max_filesize'));
    $b = ini_bytes((string)ini_get('post_max_size'));
    if ($a <= 0 && $b <= 0) return 0;
    if ($a <= 0) return (int)floor($b / 1048576);
    if ($b <= 0) return (int)floor($a / 1048576);
    return (int)floor(min($a, $b) / 1048576);
}

function video_config_default(): array
{
    return [
        'enabled'        => 1,
        'max_mb'         => 100,
        'total_limit_mb' => 600,
        'keep_days'      => 0,
        'auto_clean'     => 1,
    ];
}

/** 0/1 归一化 (与 lottery_flag 同一套语义) */
function video_flag($v): int
{
    return lottery_flag($v);
}

/** 读取视频配置 (settings.video_config): 缺失/越界一律回落到合法默认值 */
function video_config(): array
{
    $cfg = video_config_default();
    try {
        $raw = setting_get('video_config', '');
        if ($raw !== '') {
            $j = json_decode($raw, true);
            if (is_array($j)) $cfg = array_merge($cfg, $j);
        }
    } catch (Exception $e) { error_log('[video_config] ' . $e->getMessage()); }
    $clamp = function ($v, $min, $max) { $v = (int)$v; if ($v < $min) $v = $min; if ($v > $max) $v = $max; return $v; };
    $cfg['enabled']        = video_flag($cfg['enabled'] ?? 1);
    $cfg['auto_clean']     = video_flag($cfg['auto_clean'] ?? 1);
    $cfg['max_mb']         = $clamp($cfg['max_mb'] ?? 100, 1, 500);
    $cfg['total_limit_mb'] = $clamp($cfg['total_limit_mb'] ?? 600, 100, 100000);
    $cfg['keep_days']      = $clamp($cfg['keep_days'] ?? 0, 0, 3650);
    return $cfg;
}

/** 保存视频配置 */
function video_config_save(array $cfg): void
{
    setting_set('video_config', json_encode($cfg, JSON_UNESCAPED_UNICODE));
}

/** 扩展名 -> 上传 Content-Type */
function video_ctype(string $ext): string
{
    $map = ['mp4' => 'video/mp4', 'mov' => 'video/quicktime', 'mkv' => 'video/x-matroska', 'webm' => 'video/webm'];
    return $map[$ext] ?? 'application/octet-stream';
}

/** 视频真实性判定: finfo 优先, 再用文件头兜底, 最后看文件名后缀; 不支持返回 '' */
function video_detect_ext(string $raw, string $name = ''): string
{
    $byMime = [
        'video/mp4'        => 'mp4',
        'video/quicktime'  => 'mov',
        'video/x-matroska' => 'mkv',
        'video/webm'       => 'webm',
        'video/x-m4v'      => 'mp4',
        'application/mp4'  => 'mp4',
    ];
    if (function_exists('finfo_open')) {
        $fi = @finfo_open(FILEINFO_MIME_TYPE);
        if ($fi) {
            $mime = strtolower(trim((string)@finfo_buffer($fi, $raw)));
            @finfo_close($fi);
            if (isset($byMime[$mime])) return $byMime[$mime];
        }
    }
    $head = substr($raw, 0, 4096);
    // mp4 / mov: 认 ftyp box。正常在 4~8 字节, 少数文件前面还有 free/wide box, 所以在前 4KB 里找。
    // brand 'qt  ' = QuickTime; m4a/m4b/m4p 是纯音频, 不算视频
    $pos = strpos($head, 'ftyp');
    if ($pos !== false && $pos >= 4) {
        $brand = strtolower(substr($head, $pos + 4, 4));
        if (strpos($brand, 'm4a') !== 0 && strpos($brand, 'm4b') !== 0 && strpos($brand, 'm4p') !== 0) {
            return $brand === 'qt  ' ? 'mov' : 'mp4';
        }
    }
    // mkv / webm: EBML 头 1A 45 DF A3, DocType 里带 webm 就是 webm
    if (strlen($raw) >= 4 && substr($raw, 0, 4) === "\x1A\x45\xDF\xA3") {
        return stripos($head, 'webm') !== false ? 'webm' : 'mkv';
    }
    // 认不出容器就返回空: 不再拿扩展名兜底 (否则随便一个文件改名成 .mp4 就能传上来)
    return '';
}

/** 视频尺寸/时长/大小参数 (客户端传值, 服务端只做范围兜底): 返回 [w, h, duration, size] */
function video_meta_params(): array
{
    $w = (int)param('video_w', 0);
    $h = (int)param('video_h', 0);
    $d = (int)param('video_duration', 0);
    $s = (int)param('video_size', 0);
    if ($w < 0 || $w > 10000 || $h < 0 || $h > 10000) json_error('视频尺寸参数不合法');
    if ($d < 0 || $d > 3600) json_error('视频时长参数不合法 (最长 60 分钟)');
    if ($s < 0 || $s > 2147483647) json_error('视频大小参数不合法');
    return [$w, $h, $d, $s];
}

/** 从对象存储地址反推 key (只接受本站地址, 其它一律返回 '') */
function video_key_from_url(string $url): string
{
    $prefix = S3_PUBLIC_URL . '/';
    if ($url === '' || strpos($url, $prefix) !== 0) return '';
    return substr($url, strlen($prefix));
}

/**
 * 视频占用统计: 条数 / 总字节 / 最大单条 / 最老一条 / 已清理条数
 * 口径: 只统计 video 非空的行 (清理过的行 video 已置空, 不会再重复计入)
 */
function video_usage_stats(): array
{
    $out = ['count' => 0, 'total_bytes' => 0, 'max_size' => 0, 'max_size_id' => 0,
            'oldest_at' => '', 'oldest_id' => 0, 'cleaned_count' => 0];
    foreach (['social_messages', 'social_pm_messages'] as $tb) {
        try {
            $st = db()->query("SELECT COUNT(*) AS c, COALESCE(SUM(video_size), 0) AS s FROM `$tb`
                               WHERE video IS NOT NULL AND video <> ''");
            $r = $st->fetch() ?: [];
            $out['count'] += (int)($r['c'] ?? 0);
            $out['total_bytes'] += (int)($r['s'] ?? 0);
            $st = db()->query("SELECT id, video_size FROM `$tb` WHERE video IS NOT NULL AND video <> ''
                               ORDER BY video_size DESC LIMIT 1");
            $r = $st->fetch();
            if ($r && (int)$r['video_size'] > (int)$out['max_size']) {
                $out['max_size'] = (int)$r['video_size'];
                $out['max_size_id'] = (int)$r['id'];
            }
            $st = db()->query("SELECT id, created_at FROM `$tb` WHERE video IS NOT NULL AND video <> ''
                               ORDER BY created_at ASC, id ASC LIMIT 1");
            $r = $st->fetch();
            if ($r && ((string)$out['oldest_at'] === '' || (string)$r['created_at'] < (string)$out['oldest_at'])) {
                $out['oldest_at'] = (string)$r['created_at'];
                $out['oldest_id'] = (int)$r['id'];
            }
            $st = db()->query("SELECT COUNT(*) FROM `$tb` WHERE (video IS NULL OR video = '')
                               AND content LIKE '%[视频已清理]%'");
            $out['cleaned_count'] += (int)$st->fetchColumn();
        } catch (Exception $e) { error_log('[video_usage] ' . $tb . ' ' . $e->getMessage()); }
    }
    return $out;
}

/**
 * 视频清理主逻辑 (群消息 + 私聊消息一起算)
 *   - $byCapacity: 总字节 > total_limit_mb 时按 created_at ASC 删最旧的
 *   - $byDays:     keep_days > 0 时删掉超过保留天数的
 *   - $respectAutoClean: true 时 auto_clean=0 只统计不删 (上传后的自动清理走这个)
 * 消息记录保留: video 置空 + content 追加 [视频已清理]; video_size 保留做审计
 *
 * @return array{deleted:int, freed_bytes:int, visited:int, skipped:string}
 */
function video_cleanup(bool $byCapacity, bool $byDays, bool $respectAutoClean): array
{
    $out = ['deleted' => 0, 'freed_bytes' => 0, 'visited' => 0, 'skipped' => ''];
    $cfg = video_config();
    $rows = [];
    foreach (['social_messages', 'social_pm_messages'] as $tb) {
        try {
            $st = db()->query("SELECT id, video, video_size, created_at FROM `$tb`
                               WHERE video IS NOT NULL AND video <> '' ORDER BY created_at ASC, id ASC");
            foreach ($st->fetchAll() as $r) {
                $rows[] = ['table' => $tb, 'id' => (int)$r['id'], 'video' => (string)$r['video'],
                           'size' => (int)$r['video_size'], 'created_at' => (string)$r['created_at']];
            }
        } catch (Exception $e) { error_log('[video_cleanup] ' . $tb . ' ' . $e->getMessage()); }
    }
    $out['visited'] = count($rows);
    if (!$rows) return $out;
    if ($respectAutoClean && (int)$cfg['auto_clean'] !== 1) {
        $out['skipped'] = 'auto_clean=0 只统计不删除';
        return $out;
    }
    $total = 0;
    foreach ($rows as $r) { $total += $r['size']; }
    $limitBytes = (int)$cfg['total_limit_mb'] * 1024 * 1024;
    $deadline = ($byDays && (int)$cfg['keep_days'] > 0) ? (time() - (int)$cfg['keep_days'] * 86400) : 0;
    $doomed = [];
    foreach ($rows as $i => $r) {
        $ts = strtotime($r['created_at']);
        if ($deadline > 0 && $ts > 0 && $ts < $deadline) { $doomed[$i] = true; $total -= $r['size']; continue; }
        if ($byCapacity && $total > $limitBytes) { $doomed[$i] = true; $total -= $r['size']; }
    }
    foreach (array_keys($doomed) as $i) { video_cleanup_one($rows[$i], $out); }
    return $out;
}

/** 上传成功后的自动清理 (契约 Wave 2): auto_clean=0 时只统计不删 */
function video_cleanup_if_needed(): array
{
    return video_cleanup(true, true, true);
}

/** 清空全部视频 (后台「清空全部视频」): 消息保留, video 置空 + content 追加 [视频已清理] */
function video_clean_all(): array
{
    $out = ['deleted' => 0, 'freed_bytes' => 0, 'visited' => 0, 'skipped' => ''];
    foreach (['social_messages', 'social_pm_messages'] as $tb) {
        try {
            $st = db()->query("SELECT id, video, video_size, created_at FROM `$tb`
                               WHERE video IS NOT NULL AND video <> '' ORDER BY created_at ASC, id ASC");
            foreach ($st->fetchAll() as $r) {
                $out['visited']++;
                video_cleanup_one(['table' => $tb, 'id' => (int)$r['id'], 'video' => (string)$r['video'],
                                   'size' => (int)$r['video_size'], 'created_at' => (string)$r['created_at']], $out);
            }
        } catch (Exception $e) { error_log('[video_clean_all] ' . $tb . ' ' . $e->getMessage()); }
    }
    return $out;
}

/** 清理单条: 删 S3 对象 + 消息里 video 置空并追加 [视频已清理] */
function video_cleanup_one(array $row, array &$out): void
{
    $key = video_key_from_url((string)$row['video']);
    if ($key !== '') @s3_delete($key);
    try {
        db()->prepare("UPDATE `{$row['table']}` SET video = '', content = CONCAT(content, '[视频已清理]') WHERE id = ?")
            ->execute([(int)$row['id']]);
    } catch (Exception $e) {
        error_log('[video_cleanup_one] ' . $e->getMessage());
        return;
    }
    $out['deleted']++;
    $out['freed_bytes'] += (int)$row['size'];
}

function setting_get(string $key, string $default = ''): string
{
    $st = db()->prepare('SELECT `value` FROM settings WHERE `key` = ?');
    $st->execute([$key]);
    $v = $st->fetchColumn();
    return $v === false || $v === null ? $default : (string)$v;
}

function setting_set(string $key, string $value): void
{
    db()->prepare('INSERT INTO settings (`key`, `value`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)')
        ->execute([$key, $value]);
}

function mute_left_text(?array $mute): string {
    if (!$mute) return '';
    if (empty($mute['until_at'])) return '永久';
    $left = strtotime($mute['until_at']) - time();
    if ($left <= 0) return '';
    if ($left >= 86400) return '剩余 ' . max(1, (int)floor($left / 86400)) . ' 天';
    if ($left >= 3600) return '剩余 ' . max(1, (int)floor($left / 3600)) . ' 小时';
    return '剩余 ' . max(1, (int)ceil($left / 60)) . ' 分钟';
}

function my_muted_ids(): array
{
    $u = current_user();
    if (!$u) return [];
    try {
        $st = db()->prepare('SELECT group_id FROM social_mutes WHERE user_id = ?');
        $st->execute([(int)$u['id']]);
        return array_flip(array_map('intval', $st->fetchAll(PDO::FETCH_COLUMN)));
    } catch (Exception $e) {
        return [];
    }
}

/**
 * 当前用户在各个群里的未读情况: [gid => ['count' => 条数, 'first_id' => 第一条未读消息 id]]
 * 只统计别人发的、未撤回的、且 id 大于自己 last_read_id 的消息; 未登录返回空数组
 */
/** 每个群最后一条没被撤回的消息 (群列表显示「最新消息」用) */
function group_last_messages(array $gids): array
{
    $gids = array_values(array_filter(array_map('intval', $gids)));
    if (!$gids) return [];
    $in = implode(',', array_fill(0, count($gids), '?'));
    $vselLast = db_has_column('social_messages', 'video')
        ? ', m.video, m.video_w, m.video_h, m.video_duration, m.video_size' : '';
    $st = db()->prepare("SELECT m.id, m.group_id, m.user_id, m.content, m.image, m.msg_type" . $vselLast . ", m.created_at,
                u.nickname, u.username
            FROM social_messages m LEFT JOIN users u ON u.id = m.user_id
            WHERE m.is_recalled = 0
              AND m.id = (SELECT MAX(x.id) FROM social_messages x WHERE x.group_id = m.group_id AND x.is_recalled = 0)
              AND m.group_id IN ($in)");
    $st->execute($gids);
    $out = [];
    foreach ($st->fetchAll() as $r) { $out[(int)$r['group_id']] = $r; }
    return $out;
}

/**
 * 每个群里「@我」与「@所有人」的未读条数与第一条消息 id (群里没人 @ 的群不会出现在结果里)。
 * 返回 [gid => ['at_me'=>n,'at_me_first'=>id,'at_all'=>n,'at_all_first'=>id]]
 */
function my_mention_map(): array
{
    $u = current_user();
    if (!$u) return [];
    $meId = (int)$u['id'];
    try {
        $st = db()->prepare('SELECT m.id, m.group_id, m.at_users
            FROM social_messages m
            LEFT JOIN social_reads r ON r.group_id = m.group_id AND r.user_id = ?
            WHERE m.is_recalled = 0 AND m.user_id <> ? AND m.at_users <> ?
              AND m.id > COALESCE(r.last_read_id, 0)
            ORDER BY m.id ASC');
        $st->execute([$meId, $meId, '']);
        $out = [];
        foreach ($st->fetchAll() as $r) {
            $ids = array_map('intval', explode(',', (string)$r['at_users']));
            $gid = (int)$r['group_id'];
            if (!isset($out[$gid])) $out[$gid] = ['at_me' => 0, 'at_me_first' => 0, 'at_all' => 0, 'at_all_first' => 0];
            if (in_array(0, $ids, true)) {
                $out[$gid]['at_all']++;
                if ((int)$out[$gid]['at_all_first'] === 0) $out[$gid]['at_all_first'] = (int)$r['id'];
            }
            if (in_array($meId, $ids, true)) {
                $out[$gid]['at_me']++;
                if ((int)$out[$gid]['at_me_first'] === 0) $out[$gid]['at_me_first'] = (int)$r['id'];
            }
        }
        return $out;
    } catch (Exception $e) { return []; }
}

// ==================== 私聊辅助 ====================

/** 取(不存在则创建)两个用户之间的会话 id; $create=false 时不存在返回 0 */
function pm_conv_id(int $a, int $b, bool $create = true): int
{
    if ($a === $b) return 0;
    $lo = min($a, $b); $hi = max($a, $b);
    $st = db()->prepare('SELECT id FROM social_pms WHERE user_a = ? AND user_b = ?');
    $st->execute([$lo, $hi]);
    $id = (int)($st->fetchColumn() ?: 0);
    if ($id > 0 || !$create) return $id;
    try {
        db()->prepare('INSERT INTO social_pms (user_a, user_b, created_at) VALUES (?, ?, NOW())')->execute([$lo, $hi]);
    } catch (Exception $e) { /* 并发下已存在, 忽略 */ }
    $st->execute([$lo, $hi]);
    return (int)($st->fetchColumn() ?: 0);
}

/** 会话里对方的 user id */
function pm_other_id(array $conv, int $meId): int
{
    return ((int)$conv['user_a'] === $meId) ? (int)$conv['user_b'] : (int)$conv['user_a'];
}

/** 私聊消息 -> 客户端结构 */
function pm_msg_public(array $m, int $meId): array
{
    $recalled = (int)($m['is_recalled'] ?? 0) === 1;
    $vurl = (string)($m['video'] ?? '');
    $mtype = (string)($m['msg_type'] ?? '');
    if ($mtype === '' && $vurl !== '') $mtype = 'video';
    return [
        'id'          => (int)$m['id'],
        'conv_id'     => (int)$m['conv_id'],
        'user_id'     => (int)$m['from_user'],
        'to_user'     => (int)$m['to_user'],
        'content'     => $recalled ? '' : (string)($m['content'] ?? ''),
        'image'       => $recalled ? '' : (string)($m['image'] ?? ''),
        'image_w'     => $recalled ? 0 : (int)($m['image_w'] ?? 0),
        'image_h'     => $recalled ? 0 : (int)($m['image_h'] ?? 0),
        'msg_type'    => $recalled ? '' : $mtype,
        'video'          => $recalled ? '' : $vurl,
        'video_w'        => $recalled ? 0 : (int)($m['video_w'] ?? 0),
        'video_h'        => $recalled ? 0 : (int)($m['video_h'] ?? 0),
        'video_duration' => $recalled ? 0 : (int)($m['video_duration'] ?? 0),
        'video_size'     => $recalled ? 0 : (int)($m['video_size'] ?? 0),
        'is_recalled' => $recalled ? 1 : 0,
        'mine'        => ((int)$m['from_user'] === $meId) ? 1 : 0,
        'created_at'  => (string)($m['created_at'] ?? ''),
    ];
}

/** 我的私聊未读: [conv_id => ['count'=>n,'first_id'=>id]] */
function pm_unread_map(): array
{
    $u = current_user();
    if (!$u) return [];
    $meId = (int)$u['id'];
    try {
        $st = db()->prepare('SELECT m.conv_id, COUNT(*) AS c, MIN(m.id) AS first_id
            FROM social_pm_messages m
            LEFT JOIN social_pm_reads r ON r.conv_id = m.conv_id AND r.user_id = ?
            WHERE m.to_user = ? AND m.is_recalled = 0 AND m.id > COALESCE(r.last_read_id, 0)
            GROUP BY m.conv_id');
        $st->execute([$meId, $meId]);
        $out = [];
        foreach ($st->fetchAll() as $r) {
            $out[(int)$r['conv_id']] = ['count' => (int)$r['c'], 'first_id' => (int)$r['first_id']];
        }
        return $out;
    } catch (Exception $e) { return []; }
}

/** 用户简要资料 */
function user_brief(array $u): array
{
    return [
        'id'       => (int)$u['id'],
        'username' => (string)($u['username'] ?? ''),
        'nickname' => (string)(($u['nickname'] ?? '') !== '' ? $u['nickname'] : ($u['username'] ?? '')),
        'avatar'   => (string)($u['avatar'] ?? ''),
        'tags'     => user_tags_arr($u),
    ];
}

function my_unread_map(): array
{
    $u = current_user();
    if (!$u) return [];
    try {
        $st = db()->prepare('SELECT m.group_id, COUNT(*) AS c, MIN(m.id) AS first_id
                FROM social_messages m
                LEFT JOIN social_reads r ON r.group_id = m.group_id AND r.user_id = ?
                WHERE m.is_recalled = 0 AND m.user_id <> ? AND m.id > COALESCE(r.last_read_id, 0)
                      AND (m.msg_type IS NULL OR m.msg_type <> ?)
                GROUP BY m.group_id');
        $st->execute([(int)$u['id'], (int)$u['id'], 'system']);
        $out = [];
        foreach ($st->fetchAll() as $r) {
            $out[(int)$r['group_id']] = ['count' => (int)$r['c'], 'first_id' => (int)$r['first_id']];
        }
        return $out;
    } catch (Exception $e) {
        return [];
    }
}

/** 某个群里开了免打扰的用户 id 集合 (key = user_id) */
function mute_user_ids(int $gid): array
{
    try {
        $st = db()->prepare('SELECT user_id FROM social_mutes WHERE group_id = ?');
        $st->execute([$gid]);
        return array_flip(array_map('intval', $st->fetchAll(PDO::FETCH_COLUMN)));
    } catch (Exception $e) {
        return [];
    }
}

/**
 * 合并推送: 同一用户已经有同标题的未读通知时只更新它, 不再堆新的。
 * 群消息就靠这个做成「「X」新消息」一条, 和微信/QQ 一样不会刷屏。
 */
function notify_merge(int $userId, string $title, string $content, string $type = 'social', string $link = ''): void
{
    if ($userId <= 0) return;
    try {
        $st = db()->prepare('SELECT id FROM notifications WHERE user_id = ? AND type = ? AND title = ? AND is_read = 0 ORDER BY id DESC LIMIT 1');
        $st->execute([$userId, $type, $title]);
        $id = (int)($st->fetchColumn() ?: 0);
        if ($id > 0) {
            db()->prepare('UPDATE notifications SET content = ?, link = ?, created_at = NOW() WHERE id = ?')
                ->execute([$content, $link, $id]);
        } else {
            db()->prepare('INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)')
                ->execute([$userId, $title, $content, $type, $link]);
        }
    } catch (Exception $e) {
        error_log('[notify_merge] ' . $e->getMessage());
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


// ==================== 抽奖系统辅助函数 ====================

/** 抽奖配置文本字段长度统一定义(错误文案) */
function lottery_cfg_ts(?int $ts = null): int
{
    if ($ts !== null) return $ts;
    try {
        $st = db()->query('SELECT UNIX_TIMESTAMP(NOW())');
        $v = (int)$st->fetchColumn();
        if ($v > 0) return $v;
    } catch (Exception $e) { error_log('[lottery_cfg_ts] ' . $e->getMessage()); }
    return time();
}

/** 0/1 开关归一化: 兼容 bool / 1 / 0 / "1" / "0" / "true" / "false" / "on" / "off" (JSON body 里可能是布尔) */
function lottery_flag($v): int
{
    if (is_bool($v)) return $v ? 1 : 0;
    if (is_int($v) || is_float($v)) return ((int)$v === 1) ? 1 : 0;
    $s = strtolower(trim((string)$v));
    return in_array($s, ['1', 'true', 'on', 'yes'], true) ? 1 : 0;
}

/** 把 HH:MM 解析成 [h, m], 不合法返回 null */
function lottery_cfg_hm(string $v): ?array
{
    if (preg_match('/^(\d{1,2}):(\d{1,2})$/', trim($v), $m)) {
        $h = (int)$m[1]; $i = (int)$m[2];
        if ($h >= 0 && $h <= 23 && $i >= 0 && $i <= 59) return [$h, $i];
    }
    return null;
}

/** 按 daily_reset_time 切分的「今日」区间 [startTs, endTs) */
function lottery_day_window(array $cfg, ?int $ts = null): array
{
    $ts = lottery_cfg_ts($ts);
    $hm = lottery_cfg_hm((string)($cfg['daily_reset_time'] ?? '00:00'));
    if (!$hm) $hm = [0, 0];
    $b = mktime($hm[0], $hm[1], 0, (int)date('n', $ts), (int)date('j', $ts), (int)date('Y', $ts));
    if ($b !== false && $b > $ts) {
        $b = mktime($hm[0], $hm[1], 0, (int)date('n', $ts), (int)date('j', $ts) - 1, (int)date('Y', $ts));
    }
    return [$b, $b + 86400];
}

/** 本周区间 [startTs, endTs): 周一(按 daily_reset_time 时刻)起算 */
function lottery_week_window(array $cfg, ?int $ts = null): array
{
    $ts = lottery_cfg_ts($ts);
    $hm = lottery_cfg_hm((string)($cfg['daily_reset_time'] ?? '00:00'));
    if (!$hm) $hm = [0, 0];
    $monday = mktime($hm[0], $hm[1], 0, (int)date('n', $ts), (int)date('j', $ts), (int)date('Y', $ts));
    if ($monday !== false && $monday > $ts) $monday -= 86400;
    $dow = (int)date('N', $monday); // 1=周一 .. 7=周日
    $start = $monday - ($dow - 1) * 86400;
    return [$start, $start + 7 * 86400];
}

/** 某时刻是否命中开放时段 (不含日期范围/开关判断) */
function lottery_window_ts_open(array $cfg, int $ts): bool
{
    if ((int)($cfg['windows_enabled'] ?? 0) !== 1) return true;
    $ws = is_array($cfg['windows'] ?? null) ? $cfg['windows'] : [];
    if (!$ws) return true;
    $cur = (int)date('G', $ts) * 60 + (int)date('i', $ts);
    $dow = (int)date('N', $ts);   // 1=周一
    $prevDow = $dow === 1 ? 7 : $dow - 1;
    foreach ($ws as $w) {
        if (!is_array($w)) continue;
        $a = lottery_cfg_hm((string)($w['start'] ?? ''));
        $b = lottery_cfg_hm((string)($w['end'] ?? ''));
        if (!$a || !$b) continue;
        $days = isset($w['days']) && is_array($w['days']) ? $w['days'] : [];
        $s = $a[0] * 60 + $a[1];
        $e = $b[0] * 60 + $b[1];
        $has = function ($d) use ($days) {
            if (!$days) return true;
            foreach ($days as $x) { if ((int)$x === $d) return true; }
            return false;
        };
        if ($s === $e) { if ($has($dow)) return true; continue; }
        if ($s < $e) {
            if ($has($dow) && $cur >= $s && $cur < $e) return true;
        } else {
            if ($has($dow) && $cur >= $s) return true;          // 今天的段跨到明天凌晨
            if ($has($prevDow) && $cur < $e) return true;       // 今天凌晨属于昨天开始的段
        }
    }
    return false;
}

/** 日期范围判断: '' / -1 可抽, 1 未开始, 2 已结束 */
function lottery_date_range_state(array $cfg, int $ts): int
{
    $today = date('Y-m-d', $ts);
    $sd = (string)($cfg['start_date'] ?? '');
    $ed = (string)($cfg['end_date'] ?? '');
    if ($sd !== '' && $today < $sd) return 1;
    if ($ed !== '' && $today > $ed) return 2;
    return -1;
}

/** 距离最近一次开放(现在已开放则 0), 扫描 8 天; -1 表示 8 天内没有可开放的时段 */
function lottery_next_open_seconds(array $cfg, int $ts): int
{
    if ((int)($cfg['windows_enabled'] ?? 0) !== 1) return 0;
    $ws = is_array($cfg['windows'] ?? null) ? $cfg['windows'] : [];
    if (!$ws) return 0;
    if (lottery_window_ts_open($cfg, $ts)) return 0;
    $base = $ts - ($ts % 60);
    for ($k = 1; $k <= 11520; $k++) {
        $t = $base + $k * 60;
        if (lottery_window_ts_open($cfg, $t)) return $t - $ts;
    }
    return -1;
}

/** 把 windows 配置拼成给用户看的文案: 每天 19:30-20:00 */
function lottery_windows_text(array $cfg): string
{
    if ((int)($cfg['windows_enabled'] ?? 0) !== 1) return '';
    $ws = is_array($cfg['windows'] ?? null) ? $cfg['windows'] : [];
    if (!$ws) return '';
    $names = [1 => '周一', 2 => '周二', 3 => '周三', 4 => '周四', 5 => '周五', 6 => '周六', 7 => '周日'];
    $out = [];
    foreach ($ws as $w) {
        if (!is_array($w)) continue;
        $a = lottery_cfg_hm((string)($w['start'] ?? ''));
        $b = lottery_cfg_hm((string)($w['end'] ?? ''));
        if (!$a || !$b) continue;
        $days = isset($w['days']) && is_array($w['days']) ? array_values(array_unique(array_map('intval', $w['days']))) : [];
        sort($days);
        $ds = [];
        foreach ($days as $d) { if (isset($names[$d])) $ds[] = $names[$d]; }
        $prefix = $ds ? implode('、', $ds) : '每天';
        $out[] = $prefix . ' ' . sprintf('%02d:%02d', $a[0], $a[1]) . '-' . sprintf('%02d:%02d', $b[0], $b[1]);
    }
    return implode('; ', $out);
}

/** 抽奖开放状态全量 */
function lottery_window_state(array $cfg, ?int $ts = null): array
{
    $now = lottery_cfg_ts($ts);
    $serverTime = date('Y-m-d H:i:s', $now);
    $res = [
        'open' => false, 'reason' => 'open', 'reason_text' => '', 'text' => '',
        'next_open_at' => '', 'next_close_at' => '',
        'seconds_to_open' => 0, 'seconds_to_close' => 0,
        'server_time' => $serverTime, 'my_allowed' => true,
    ];
    if ((int)($cfg['enabled'] ?? 0) !== 1) {
        $res['reason'] = 'disabled'; $res['reason_text'] = '抽奖活动已关闭'; $res['my_allowed'] = false;
        return $res;
    }
    $range = lottery_date_range_state($cfg, $now);
    if ($range === 1) {
        $res['reason'] = 'before_start'; $res['reason_text'] = '抽奖活动还没开始'; $res['my_allowed'] = false;
        return $res;
    }
    if ($range === 2) {
        $res['reason'] = 'after_end'; $res['reason_text'] = '抽奖活动已经结束'; $res['my_allowed'] = false;
        return $res;
    }
    if ((int)($cfg['windows_enabled'] ?? 0) !== 1 || !is_array($cfg['windows'] ?? null) || !$cfg['windows']) {
        $res['open'] = true; $res['reason'] = 'open'; $res['reason_text'] = ''; $res['text'] = '';
        return $res;
    }
    $res['text'] = lottery_windows_text($cfg);
    if (!lottery_window_ts_open($cfg, $now)) {
        $left = lottery_next_open_seconds($cfg, $now);
        if ($left > 0) {
            $res['seconds_to_open'] = $left;
            $res['next_open_at'] = date('Y-m-d H:i:s', $now + $left);
        }
        $res['reason'] = 'outside_window';
        $res['reason_text'] = '现在不在抽奖时间内';
        $res['my_allowed'] = false;
        return $res;
    }
    $res['open'] = true; $res['reason'] = 'open'; $res['reason_text'] = '';
    $k = 1;
    while ($k <= 11520 && lottery_window_ts_open($cfg, $now + $k * 60)) $k++;
    if ($k <= 11520) {
        $res['seconds_to_close'] = $k * 60 - ($now % 60);
        $res['next_close_at'] = date('Y-m-d H:i:s', $now + $res['seconds_to_close']);
    }
    return $res;
}

/** 距上次抽奖还差多少秒 (0 = 可以抽) */
function lottery_cooldown_left(array $me): int
{
    $cfg = lottery_config();
    $cd = (int)($cfg['cooldown_seconds'] ?? 0);
    if ($cd <= 0) return 0;
    try {
        $st = db()->prepare('SELECT UNIX_TIMESTAMP(MAX(created_at)) FROM lottery_draws WHERE user_id = ?');
        $st->execute([(int)$me['id']]);
        $last = (int)($st->fetchColumn() ?: 0);
    } catch (Exception $e) { error_log('[lottery_cooldown_left] ' . $e->getMessage()); return 0; }
    if ($last <= 0) return 0;
    $left = $last + $cd - lottery_cfg_ts();
    return $left > 0 ? $left : 0;
}

/** 今天全站还能发出多少张; 返回 -1 表示不限 */
function lottery_daily_total_left(?array $cfg = null): int
{
    if ($cfg === null) $cfg = lottery_config();
    $lim = (int)($cfg['daily_total_limit'] ?? 0);
    if ($lim <= 0) return -1;
    list($ws, $we) = lottery_day_window($cfg);
    try {
        $st = db()->prepare('SELECT COUNT(*) FROM lottery_draws WHERE created_at >= ? AND created_at < ?');
        $st->execute([date('Y-m-d H:i:s', $ws), date('Y-m-d H:i:s', $we)]);
        $n = (int)$st->fetchColumn();
    } catch (Exception $e) { error_log('[lottery_daily_total_left] ' . $e->getMessage()); return $lim; }
    $left = $lim - $n;
    return $left > 0 ? $left : 0;
}

/** 某用户本周抽了几次 (按 week_window 边界) */
function lottery_drawn_count_week(int $userId): int
{
    $cfg = lottery_config();
    list($ws, $we) = lottery_week_window($cfg);
    $st = db()->prepare('SELECT COUNT(*) FROM lottery_draws d
            LEFT JOIN users u ON u.id = d.user_id
            WHERE d.user_id = ? AND d.created_at >= ? AND d.created_at < ?
              AND (u.lottery_day_reset_at IS NULL OR d.created_at > u.lottery_day_reset_at)');
    $st->execute([$userId, date('Y-m-d H:i:s', $ws), date('Y-m-d H:i:s', $we)]);
    return (int)$st->fetchColumn();
}

/** 某用户本周还能抽几次; 返回 -1 表示不限 */
function lottery_weekly_left_for(array $user): int
{
    $wk = (int)(lottery_config()['week_limit'] ?? 0);
    if ($wk <= 0) return -1;
    return max(0, $wk - lottery_drawn_count_week((int)$user['id']));
}

/** 抽奖活动配置 (存在 settings.lottery_config) */
function lottery_config(): array
{
    $cfg = [
        'enabled'          => 0,
        'title'            => '免费抽卡密',
        'content'          => '',
        'success_text'     => '',
        'empty_text'       => '奖品已抽完, 请稍后再来',
        'per_user_limit'   => 1,
        'daily_limit'      => 0,
        'week_limit'       => 0,
        'daily_reset_time' => '00:00',
        'cooldown_seconds' => 0,
        'daily_total_limit' => 0,
        'windows_enabled'  => 0,
        'windows'          => [],
        'start_date'       => '',
        'end_date'         => '',
        'show_prizes'      => 1,
        'show_stock'       => 1,
        'notify_winner'    => 1,
    ];
    try {
        $st = db()->prepare('SELECT `value` FROM `settings` WHERE `key` = ?');
        $st->execute(['lottery_config']);
        $raw = (string)($st->fetchColumn() ?: '');
        if ($raw !== '') {
            $j = json_decode($raw, true);
            if (is_array($j)) $cfg = array_merge($cfg, $j);
        }
    } catch (Exception $e) { error_log('[lottery_config] ' . $e->getMessage()); }
    $clamp = function ($v, $min, $max) { $v = (int)$v; if ($v < $min) $v = $min; if ($v > $max) $v = $max; return $v; };
    $cfg['enabled'] = lottery_flag($cfg['enabled']);
    $cfg['title'] = (string)$cfg['title'];
    if ($cfg['title'] === '') $cfg['title'] = '免费抽卡密';
    $cfg['content'] = (string)$cfg['content'];
    $cfg['success_text'] = (string)($cfg['success_text'] ?? '');
    $cfg['empty_text'] = (string)($cfg['empty_text'] ?? '');
    if ($cfg['empty_text'] === '') $cfg['empty_text'] = '奖品已抽完, 请稍后再来';
    $cfg['per_user_limit'] = $clamp($cfg['per_user_limit'] ?? 1, 0, 9999);
    $cfg['daily_limit'] = $clamp($cfg['daily_limit'] ?? 0, 0, 999);
    $cfg['week_limit'] = $clamp($cfg['week_limit'] ?? 0, 0, 999);
    $cfg['cooldown_seconds'] = $clamp($cfg['cooldown_seconds'] ?? 0, 0, 86400);
    $cfg['daily_total_limit'] = $clamp($cfg['daily_total_limit'] ?? 0, 0, 999999);
    $rt = (string)($cfg['daily_reset_time'] ?? '00:00');
    if (!lottery_cfg_hm($rt)) $rt = '00:00';
    $hm = lottery_cfg_hm($rt);
    $cfg['daily_reset_time'] = sprintf('%02d:%02d', $hm[0], $hm[1]);
    $cfg['windows_enabled'] = lottery_flag($cfg['windows_enabled'] ?? 0);
    $wins = [];
    if (is_array($cfg['windows'] ?? null)) {
        foreach ($cfg['windows'] as $w) {
            if (!is_array($w)) continue;
            $a = lottery_cfg_hm((string)($w['start'] ?? ''));
            $b = lottery_cfg_hm((string)($w['end'] ?? ''));
            if (!$a || !$b) continue;
            if ($a[0] * 60 + $a[1] === $b[0] * 60 + $b[1]) continue;
            $days = [];
            if (isset($w['days']) && is_array($w['days'])) {
                foreach ($w['days'] as $d) { $d = (int)$d; if ($d >= 1 && $d <= 7) $days[] = $d; }
                $days = array_values(array_unique($days)); sort($days);
            }
            $wins[] = ['start' => sprintf('%02d:%02d', $a[0], $a[1]), 'end' => sprintf('%02d:%02d', $b[0], $b[1]), 'days' => $days];
            if (count($wins) >= 10) break;
        }
    }
    $cfg['windows'] = $wins;
    $sd = (string)($cfg['start_date'] ?? '');
    $cfg['start_date'] = preg_match('/^\d{4}-\d{2}-\d{2}$/', $sd) ? $sd : '';
    $ed = (string)($cfg['end_date'] ?? '');
    $cfg['end_date'] = preg_match('/^\d{4}-\d{2}-\d{2}$/', $ed) ? $ed : '';
    $cfg['show_prizes'] = lottery_flag($cfg['show_prizes'] ?? 1);
    $cfg['show_stock'] = lottery_flag($cfg['show_stock'] ?? 1);
    $cfg['notify_winner'] = lottery_flag($cfg['notify_winner'] ?? 1);
    return $cfg;
}

/** 保存抽奖活动配置 */
function lottery_config_save(array $cfg): void
{
    $json = json_encode($cfg, JSON_UNESCAPED_UNICODE);
    db()->prepare('INSERT INTO `settings` (`key`, `value`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)')
        ->execute(['lottery_config', $json]);
}

/** 后台保存抽奖配置时的逐项校验 (错误文案固定, 见契约 B 节) */
function lottery_config_validate_time($v): string
{
    $v = trim((string)$v);
    if (!lottery_cfg_hm($v)) json_error('每日重置时间格式不对, 应该是 HH:MM');
    $hm = lottery_cfg_hm($v);
    return sprintf('%02d:%02d', $hm[0], $hm[1]);
}

function lottery_config_validate_text(string $field, $v, int $max): string
{
    $v = (string)$v;
    if (mb_strlen($v) > $max) json_error($field . '不能超过 ' . $max . ' 个字');
    return $v;
}

/** 校验 windows 列表, 返回归一化后的数组 */
function lottery_config_validate_windows($ws): array
{
    if (!is_array($ws)) json_error('抽奖时段格式不对, 应该是 HH:MM-HH:MM');
    if (count($ws) > 10) json_error('抽奖时段最多 10 条');
    $out = [];
    foreach ($ws as $w) {
        if (!is_array($w)) json_error('抽奖时段格式不对, 应该是 HH:MM-HH:MM');
        $a = lottery_cfg_hm((string)($w['start'] ?? ''));
        $b = lottery_cfg_hm((string)($w['end'] ?? ''));
        if (!$a || !$b) json_error('抽奖时段格式不对, 应该是 HH:MM-HH:MM');
        if ($a[0] * 60 + $a[1] === $b[0] * 60 + $b[1]) json_error('抽奖时段开始和结束时间不能相同');
        $days = [];
        if (isset($w['days']) && $w['days'] !== '' && $w['days'] !== null) {
            if (!is_array($w['days'])) json_error('星期只能是 1-7');
            foreach ($w['days'] as $d) {
                $d = (int)$d;
                if ($d < 1 || $d > 7) json_error('星期只能是 1-7');
                $days[] = $d;
            }
            $days = array_values(array_unique($days)); sort($days);
        }
        $out[] = [
            'start' => sprintf('%02d:%02d', $a[0], $a[1]),
            'end'   => sprintf('%02d:%02d', $b[0], $b[1]),
            'days'  => $days,
        ];
    }
    return $out;
}

/** 校验 YYYY-MM-DD, 空串放行 */
function lottery_config_validate_date(string $field, $v): string
{
    $v = trim((string)$v);
    if ($v === '') return '';
    if (!preg_match('/^\d{4}-\d{2}-\d{2}$/', $v)) json_error($field . '格式不对, 应该是 YYYY-MM-DD');
    $p = explode('-', $v);
    if (!checkdate((int)$p[1], (int)$p[2], (int)$p[0])) json_error($field . '格式不对, 应该是 YYYY-MM-DD');
    return $v;
}

function lottery_config_validate_zero(array $cfg): void
{
    if ((int)$cfg['cooldown_seconds'] > 86400) json_error('冷却时间不能超过 86400 秒');
    if ((int)$cfg['week_limit'] > 999) json_error('每周抽奖次数不能超过 999');
    if ((int)$cfg['daily_total_limit'] > 999999) json_error('今天发放上限不能超过 999999');
    if (mb_strlen((string)$cfg['success_text']) > 200) json_error('提示文案不能超过 200 个字');
    if (mb_strlen((string)$cfg['empty_text']) > 200) json_error('提示文案不能超过 200 个字');
}

/** 某用户已经抽了几次 (users.lottery_reset_at 之前的老记录不算, 这样后台能一键重置次数) */
function lottery_drawn_count(int $userId): int
{
    $st = db()->prepare('SELECT COUNT(*) FROM lottery_draws d
            LEFT JOIN users u ON u.id = d.user_id
            WHERE d.user_id = ? AND (u.lottery_reset_at IS NULL OR d.created_at > u.lottery_reset_at)');
    $st->execute([$userId]);
    return (int)$st->fetchColumn();
}

/** 某用户今天抽了几次 (按 daily_reset_time 切分的今日边界) */
function lottery_drawn_count_today(int $userId): int
{
    $cfg = lottery_config();
    list($ws, $we) = lottery_day_window($cfg);
    $st = db()->prepare('SELECT COUNT(*) FROM lottery_draws d
            LEFT JOIN users u ON u.id = d.user_id
            WHERE d.user_id = ? AND d.created_at >= ? AND d.created_at < ?
              AND (u.lottery_day_reset_at IS NULL OR d.created_at > u.lottery_day_reset_at)');
    $st->execute([$userId, date('Y-m-d H:i:s', $ws), date('Y-m-d H:i:s', $we)]);
    return (int)$st->fetchColumn();
}

/** 今天(按 daily_reset_time 边界)还能抽几次; 返回 -1 表示不限制 */
function lottery_daily_left_for(array $user): int
{
    $daily = (int)(lottery_config()['daily_limit'] ?? 0);
    if ($daily <= 0) return -1;
    return max(0, $daily - lottery_drawn_count_today((int)$user['id']));
}

/**
 * 用户还能抽几次 (总次数与每日次数取更小的那个)。
 * users.lottery_quota: -1 = 跟随活动默认每人次数; >=0 = 该用户总共可抽的次数(已抽的从里面扣)
 * settings.lottery_config.daily_limit: 0 = 不限, >0 = 每人每天最多抽几次
 */
function lottery_left_for(array $user): int
{
    $quota = array_key_exists('lottery_quota', $user) ? (int)$user['lottery_quota'] : -1;
    if ($quota < 0) $quota = (int)lottery_config()['per_user_limit'];
    $left = max(0, $quota - lottery_drawn_count((int)$user['id']));
    $dailyLeft = lottery_daily_left_for($user);
    if ($dailyLeft >= 0) $left = min($left, $dailyLeft);
    $weekLeft = lottery_weekly_left_for($user);
    if ($weekLeft >= 0) $left = min($left, $weekLeft);
    return $left;
}

/**
 * 抽一次奖: 先按奖项 weight 加权随机选一个「有库存的启用奖项」, 再从该奖项里随机取一张没用过的卡密,
 * 标记为已用并写中奖记录。全程在事务里做, 保证同一张卡密不会发给两个人。
 */
function lottery_draw_once(int $userId): array
{
    $pdo = db();
    $pdo->beginTransaction();
    try {
        $st = $pdo->prepare('SELECT p.id,p.name,p.card_type,p.weight,
                (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id AND c.user_id = 0) AS stock
            FROM lottery_prizes p WHERE p.is_active = 1');
        $st->execute();
        $pool = [];
        $total = 0;
        foreach ($st->fetchAll() as $p) {
            $stock = (int)$p['stock'];
            if ($stock <= 0) continue;
            $w = (int)($p['weight'] ?? 0);
            if ($w <= 0) $w = 100;
            $pool[] = ['id' => (int)$p['id'], 'w' => $w, 'stock' => $stock];
            $total += $w;
        }
        if ($total <= 0) { $pdo->rollBack(); json_error((string)lottery_config()['empty_text']); }
        $r = random_int(1, $total);
        $pick = $pool[0];
        foreach ($pool as $p) {
            $r -= $p['w'];
            if ($r <= 0) { $pick = $p; break; }
        }
        $st = $pdo->prepare('SELECT c.id AS code_id, c.code, p.id AS prize_id, p.name AS prize_name, p.card_type
            FROM lottery_codes c INNER JOIN lottery_prizes p ON p.id = c.prize_id
            WHERE c.user_id = 0 AND p.id = ?
            ORDER BY RAND() LIMIT 1 FOR UPDATE');
        $st->execute([$pick['id']]);
        $row = $st->fetch();
        if (!$row) { $pdo->rollBack(); json_error((string)lottery_config()['empty_text']); }
        $pdo->prepare('UPDATE lottery_codes SET user_id = ?, used_at = NOW() WHERE id = ? AND user_id = 0')
            ->execute([$userId, (int)$row['code_id']]);
        $pdo->prepare('INSERT INTO lottery_draws (user_id, prize_id, code_id, code, created_at) VALUES (?, ?, ?, ?, NOW())')
            ->execute([$userId, (int)$row['prize_id'], (int)$row['code_id'], (string)$row['code']]);
        $drawId = (int)$pdo->lastInsertId();
        $got = [
            'id'         => $drawId,
            'prize_id'   => (int)$row['prize_id'],
            'prize_name' => (string)$row['prize_name'],
            'card_type'  => (string)$row['card_type'],
            'code'       => (string)$row['code'],
        ];
        $pdo->commit();
        return $got;
    } catch (Exception $e) {
        if ($pdo->inTransaction()) $pdo->rollBack();
        throw $e;
    }
}
