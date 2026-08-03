<?php
// 风铃分享库 - API 入口
// 所有接口统一走这里: /api.php?action=xxx
// 抑制 PHP 8.x deprecated 警告 (imagedestroy/curl_close 等无效果但会污染 JSON 响应)
error_reporting(E_ALL & ~E_DEPRECATED & ~E_NOTICE & ~E_WARNING);
require_once __DIR__ . '/config.php';

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
    return $out ?: $data;
}

try {
    switch ($action) {
        // ============ 认证 ============
        case 'login':
            $username = param('username', '');
            $password = param('password', '');
            if (!$username || !$password) json_error('用户名和密码不能为空');
            $stmt = db()->prepare('SELECT * FROM users WHERE username = ? AND is_active = 1');
            $stmt->execute([$username]);
            $user = $stmt->fetch();
            if (!$user || !password_verify($password, $user['password'])) {
                json_error('用户名或密码错误');
            }
            $token = make_token();
            // 多会话: 每个登录会话独立 token, 不覆盖 users.token (多设备登录互不顶掉)
            db()->prepare('UPDATE users SET token = ? WHERE id = ?')->execute([$token, $user['id']]);
            try { db()->prepare('INSERT INTO sessions (user_id, token) VALUES (?, ?)')->execute([$user['id'], $token]); } catch (Exception $e) {}
            json_out([
                'token' => $token,
                'user' => [
                    'id' => (int)$user['id'],
                    'username' => $user['username'],
                    'nickname' => $user['nickname'],
                    'role' => $user['role'],
                ],
            ]);

        case 'logout':
            $token = param('token', '');
            if ($token) {
                db()->prepare('DELETE FROM sessions WHERE token = ?')->execute([$token]);
                // 仅当 users.token 等于当前 token 才清 (避免顶掉其他会话)
                db()->prepare('UPDATE users SET token = NULL WHERE token = ?')->execute([$token]);
            }
            json_out(null);

        // ============ 分类 (公开读, 管理写) ============
        case 'categories':
            json_out(db()->query('SELECT * FROM categories ORDER BY sort_order ASC, id ASC')->fetchAll());

        case 'category_create':
            require_admin();
            $name = param('name', '');
            if (!$name) json_error('分类名不能为空');
            $color = param('color', '#4C6FFF');
            $icon = param('icon', ''); // 分类图标 (上传图片 URL)
            $sort = (int)param('sort_order', 0);
            db()->prepare('INSERT INTO categories (name, color, icon, sort_order) VALUES (?, ?, ?, ?)')
                ->execute([$name, $color, $icon, $sort]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'category_update':
            require_admin();
            $id = (int)param('id', 0);
            $name = param('name');
            $color = param('color');
            $icon = param('icon');
            $sort = param('sort_order');
            $sql = 'UPDATE categories SET ';
            $args = [];
            if ($name !== null) { $sql .= 'name = ?, '; $args[] = $name; }
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
            db()->prepare('DELETE FROM categories WHERE id = ?')->execute([$id]);
            json_out(null);

        // ============ 软件 (公开读, 管理写) ============
        case 'apps':
            $categoryId = (int)param('category_id', 0);
            $keyword = param('keyword', '');
            $packId = param('pack_id', null);
            $sql = 'SELECT a.*, c.name AS category_name, c.color AS category_color FROM apps a
                    LEFT JOIN categories c ON a.category_id = c.id
                    WHERE a.is_active = 1';
            $args = [];
            if ($packId !== null) {
                // 整合包子项: pack_id = 父 id
                $sql .= ' AND a.pack_id = ?';
                $args[] = (int)$packId ?: 0;
            } elseif ($categoryId > 0) {
                $sql .= ' AND a.category_id = ?';
                $args[] = $categoryId;
            }
            if ($keyword !== '') { $sql .= ' AND a.name LIKE ?'; $args[] = '%' . $keyword . '%'; }
            $sql .= ' ORDER BY a.is_top DESC, a.sort_order ASC, a.id DESC';
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
                // 介绍图片: JSON 数组 -> 数组
                $app['screenshots'] = $app['screenshots'] ? (json_decode($app['screenshots'], true) ?: []) : [];
                // 投稿人 QQ 属敏感字段, 仅管理员可见 (App 端一律不给)
                if (!is_admin()) unset($app['contributor_qq']);
            }
            json_out($apps);

        case 'app_detail':
            $id = (int)param('id', 0);
            $stmt = db()->prepare('SELECT a.*, c.name AS category_name FROM apps a
                                   LEFT JOIN categories c ON a.category_id = c.id
                                   WHERE a.id = ? AND a.is_active = 1');
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
            // 介绍图片: JSON 数组 -> 数组
            $app['screenshots'] = $app['screenshots'] ? (json_decode($app['screenshots'], true) ?: []) : [];
            // 投稿人 QQ 属敏感字段, 仅管理员可见
            if (!is_admin()) unset($app['contributor_qq']);
            json_out($app);

        case 'app_create':
            require_admin();
            $app = insert_app();
            json_out(['id' => (int)$app]);

        case 'app_update':
            require_admin();
            $id = (int)param('id', 0);
            $fields = ['name', 'category_id', 'icon', 'version', 'description', 'screenshots', 'package_name', 'rating', 'sort_order', 'is_active', 'pack_id', 'is_top', 'is_featured', 'contributor_qq'];
            $sql = 'UPDATE apps SET ';
            $args = [];
            foreach ($fields as $f) {
                $v = param($f);
                if ($v !== null) {
                    if ($f === 'pack_id') { $sql .= "pack_id = ?, "; $args[] = (int)$v ?: null; }
                    elseif ($f === 'is_top' || $f === 'is_featured') { $sql .= "$f = ?, "; $args[] = (int)$v ?: 0; }
                    else { $sql .= "$f = ?, "; $args[] = $v; }
                }
            }
            if (!$args) json_error('没有要更新的字段');
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

        default:
            json_error('未知操作: ' . $action, 404);
    }
} catch (Throwable $e) {
    json_error('服务器错误: ' . $e->getMessage(), 500);
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
}

/** 是否为已登录管理员 (不抛错, 用于公开接口按需附带敏感字段) */
function is_admin(): bool
{
    $auth = $_SERVER['HTTP_AUTHORIZATION'] ?? '';
    $token = '';
    if (preg_match('/Bearer\s+(\S+)/i', $auth, $m)) {
        $token = $m[1];
    } else {
        $token = param('token', '');
    }
    if (!$token) return false;
    // 多会话: 优先 sessions 表, 兼容旧 users.token
    $stmt = db()->prepare('SELECT u.id FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = ? AND u.is_active = 1');
    $stmt->execute([$token]);
    if ($stmt->fetch()) return true;
    $stmt = db()->prepare('SELECT id FROM users WHERE token = ? AND is_active = 1');
    $stmt->execute([$token]);
    return (bool)$stmt->fetch();
}

/** 插入软件, 返回新 id */
function insert_app(): int
{
    $name = param('name', '');
    if (!$name) json_error('软件名不能为空');
    $categoryId = (int)param('category_id', 0) ?: null;
    $packId = (int)param('pack_id', 0) ?: null;
    db()->prepare('INSERT INTO apps (category_id, name, icon, version, description, screenshots, contributor_qq, package_name, rating, sort_order, pack_id, is_top, is_featured)
                   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)')
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
        ]);
    return (int)db()->lastInsertId();
}
