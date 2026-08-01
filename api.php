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
            json_out(db()->query('SELECT * FROM categories ORDER BY sort_order ASC, id ASC')->fetchAll());

        case 'category_create':
            require_admin();
            $name = param('name', '');
            if (!$name) json_error('分类名不能为空');
            $color = param('color', '#4C6FFF');
            $sort = (int)param('sort_order', 0);
            db()->prepare('INSERT INTO categories (name, color, sort_order) VALUES (?, ?, ?)')
                ->execute([$name, $color, $sort]);
            json_out(['id' => (int)db()->lastInsertId()]);

        case 'category_update':
            require_admin();
            $id = (int)param('id', 0);
            $name = param('name');
            $color = param('color');
            $sort = param('sort_order');
            $sql = 'UPDATE categories SET ';
            $args = [];
            if ($name !== null) { $sql .= 'name = ?, '; $args[] = $name; }
            if ($color !== null) { $sql .= 'color = ?, '; $args[] = $color; }
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
            $sql = 'SELECT a.*, c.name AS category_name, c.color AS category_color FROM apps a
                    LEFT JOIN categories c ON a.category_id = c.id
                    WHERE a.is_active = 1';
            $args = [];
            if ($categoryId > 0) { $sql .= ' AND a.category_id = ?'; $args[] = $categoryId; }
            if ($keyword !== '') { $sql .= ' AND a.name LIKE ?'; $args[] = '%' . $keyword . '%'; }
            $sql .= ' ORDER BY a.sort_order ASC, a.id DESC';
            $stmt = db()->prepare($sql);
            $stmt->execute($args);
            $apps = $stmt->fetchAll();
            foreach ($apps as &$app) {
                $app['download_count'] = (int)$app['download_count'];
                $app['rating'] = (float)$app['rating'];
                $app['id'] = (int)$app['id'];
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
            $app['download_count'] = (int)$app['download_count'];
            $app['rating'] = (float)$app['rating'];
            $app['id'] = (int)$app['id'];
            json_out($app);

        case 'app_create':
            require_admin();
            $app = insert_app();
            json_out(['id' => (int)$app]);

        case 'app_update':
            require_admin();
            $id = (int)param('id', 0);
            $fields = ['name', 'category_id', 'icon', 'version', 'description', 'package_name', 'rating', 'sort_order', 'is_active'];
            $sql = 'UPDATE apps SET ';
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

        case 'version':
            // 版本检测 (公开): 返回最新版本信息
            // 版本配置存 apps 表特殊记录或单独配置; 当前硬编码, 后续可加版本管理表
            $version = '1.0.0';
            $url = '';
            $update_log = '';
            $row = db()->query("SELECT * FROM settings WHERE `key` = 'latest_version'")->fetch(PDO::FETCH_ASSOC);
            if ($row) {
                $cfg = json_decode($row['value'], true);
                $version = $cfg['version'] ?? $version;
                $url = $cfg['url'] ?? '';
                $update_log = $cfg['update_log'] ?? '';
            }
            json_out([
                'version' => $version,
                'url' => $url,
                'update_log' => $update_log,
            ]);

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
    db()->prepare('INSERT INTO apps (category_id, name, icon, version, description, package_name, rating, sort_order)
                   VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
        ->execute([
            $categoryId,
            $name,
            param('icon', ''),
            param('version', ''),
            param('description', ''),
            param('package_name', ''),
            (float)param('rating', 0),
            (int)param('sort_order', 0),
        ]);
    return (int)db()->lastInsertId();
}
