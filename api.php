<?php
// 风铃分享库 - API 入口
// 所有接口统一走这里: /api.php?action=xxx
require_once __DIR__ . '/config.php';

header('Access-Control-Allow-Origin: *');
header('Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS');
header('Access-Control-Allow-Headers: Content-Type, Authorization');

if ($_SERVER['REQUEST_METHOD'] === 'OPTIONS') {
    http_response_code(204);
    exit;
}

$action = $_GET['action'] ?? '';

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
            db()->prepare('UPDATE users SET token = ? WHERE id = ?')->execute([$token, $user['id']]);
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
            if ($token) db()->prepare('UPDATE users SET token = NULL WHERE token = ?')->execute([$token]);
            json_out(null);

        // ============ 分类 (公开读, 管理写) ============
        case 'categories':
            json_out(db()->query('SELECT * FROM categories ORDER BY sort_order ASC, id ASC')->fetchAll(), 0, 'ok', 300);

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
            }
            json_out($apps, 0, 'ok', 60);

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
            json_out($app, 0, 'ok', 60);

        case 'app_create':
            require_admin();
            $app = insert_app();
            json_out(['id' => (int)$app]);

        case 'app_update':
            require_admin();
            $id = (int)param('id', 0);
            $fields = ['name', 'category_id', 'icon', 'version', 'description', 'package_name', 'rating', 'sort_order', 'is_active', 'pack_id', 'is_top', 'is_featured'];
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
            $filename = date('YmdHis') . '_' . bin2hex(random_bytes(4)) . '.' . $ext;
            $uploadDir = __DIR__ . '/uploads/';
            if (!is_dir($uploadDir)) mkdir($uploadDir, 0755, true);
            if (!move_uploaded_file($file['tmp_name'], $uploadDir . $filename)) json_error('保存失败');
            $url = 'http://' . ($_SERVER['HTTP_HOST'] ?? 'REDACTED_SERVER_HOST:9845') . '/uploads/' . $filename;
            json_out(['url' => $url, 'filename' => $filename, 'size' => $file['size']]);

        // ============ 轮播图 ============
        case 'banners':
            $rows = db()->query('SELECT b.*, a.name AS app_name FROM banners b
                                 LEFT JOIN apps a ON b.app_id = a.id
                                 WHERE b.is_active = 1 ORDER BY b.sort_order ASC, b.id ASC')->fetchAll();
            foreach ($rows as &$r) { $r['id'] = (int)$r['id']; $r['app_id'] = $r['app_id'] ? (int)$r['app_id'] : null; }
            json_out($rows, 0, 'ok', 300);

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
            json_out([
                'total_apps' => $totalApps,
                'total_downloads' => $totalDownloads,
                'total_clicks' => $totalClicks,
                'today_clicks' => $todayClicks,
                'top_links' => $topLinks,
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
            $dir = __DIR__ . '/uploads/apk';
            if (!is_dir($dir)) @mkdir($dir, 0755, true);
            $name = 'fengling_' . date('Ymd_His') . '.apk';
            $dest = $dir . '/' . $name;
            if (!move_uploaded_file($file['tmp_name'], $dest)) json_error('文件保存失败');
            $base = 'http://' . ($_SERVER['HTTP_HOST'] ?? 'REDACTED_SERVER_HOST:9845');
            json_out(['url' => $base . '/uploads/apk/' . $name, 'size' => $size]);

        case 'version_update':
            require_admin();
            $version = param('version', '');
            $url = param('url', '');
            $update_log = param('update_log', '');
            $update_mode = param('update_mode', 'internal'); // internal=内置浏览器, external=外置浏览器
            $force_update = (int)param('force_update', 0); // 1=强制更新
            // APK 大小: 未填时自动从上传文件计算
            $size_mb = (float)param('size_mb', 0);
            if ($size_mb <= 0 && $url && strpos($url, '/uploads/apk/') !== false) {
                $filePath = __DIR__ . parse_url($url, PHP_URL_PATH);
                if (file_exists($filePath)) {
                    $size_mb = round(filesize($filePath) / 1048576, 1);
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
            json_out(array_merge($defaults, $cfg), 0, 'ok', 600);

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
            ], 0, 'ok', 600);

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
    $stmt = db()->prepare('SELECT id, role FROM users WHERE token = ? AND is_active = 1');
    $stmt->execute([$token]);
    $user = $stmt->fetch();
    if (!$user) json_error('登录已失效', 401);
}

/** 插入软件, 返回新 id */
function insert_app(): int
{
    $name = param('name', '');
    if (!$name) json_error('软件名不能为空');
    $categoryId = (int)param('category_id', 0) ?: null;
    $packId = (int)param('pack_id', 0) ?: null;
    db()->prepare('INSERT INTO apps (category_id, name, icon, version, description, package_name, rating, sort_order, pack_id, is_top, is_featured)
                   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)')
        ->execute([
            $categoryId,
            $name,
            param('icon', ''),
            param('version', ''),
            param('description', ''),
            param('package_name', ''),
            (float)param('rating', 0),
            (int)param('sort_order', 0),
            $packId,
            (int)param('is_top', 0) ?: 0,
            (int)param('is_featured', 0) ?: 0,
        ]);
    return (int)db()->lastInsertId();
}
