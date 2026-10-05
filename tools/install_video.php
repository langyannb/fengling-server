<?php
/**
 * 视频消息迁移 (幂等, 可重复执行)
 *   - social_messages 增加 video / video_w / video_h / video_duration / video_size 列 (msg_type 已存在, 不动)
 *   - social_pm_messages 增加同样的 5 列 + msg_type 列
 *   - settings.video_config 不存在则写入默认值 (已存在一律不动)
 * 只加列/加配置, 不改任何已有数据; 先跑这个脚本再更新 api.php 即可。
 * 用法: /www/server/php/85/bin/php tools/install_video.php
 *
 * 另外: 100MB 级别的视频会被 Web 层的两个限制拦掉, 两处都要放开 (都是本站点级, 不动全局):
 *   1) PHP: PHP 默认 upload_max_filesize/post_max_size=50M。站点根目录 .user.ini 写上
 *        upload_max_filesize = 128M
 *        post_max_size = 140M
 *      改完 /etc/init.d/php-fpm-85 reload (user_ini.cache_ttl 默认 300s, 等缓存过期再验)。
 *      CLI 的 php -i 看不到 .user.ini, 必须用 web 语境探针确认真实生效值。
 *   2) nginx: 站点 vhost (/www/server/panel/vhost/nginx/flfxk.conf) 的
 *        client_max_body_size 必须大于「视频上限 + multipart 开销」, 否则 nginx 直接 413
 *        (nginx 的 413 是 nginx 自己返回的 HTML, 不是 api.php 的 JSON 错误)。
 *      线上实测: 100m 会被 100MB 视频的 multipart 开销顶掉, 已改为 150m;
 *        /www/server/nginx/sbin/nginx -t && /etc/init.d/nginx reload
 */
require_once __DIR__ . '/../config.php';

// 该脚本会 ALTER TABLE, 绝不能匿名触发: 只允许 CLI 运行, 或 HTTP 带管理员 token
if (PHP_SAPI !== 'cli') {
    header('Content-Type: text/plain; charset=utf-8');
    $tk = (string)($_GET['token'] ?? '');
    $ok = false;
    if ($tk !== '') {
        $st = db()->prepare('SELECT COUNT(*) FROM users WHERE role = "admin" AND is_active = 1 AND token = ?');
        $st->execute([$tk]);
        $ok = ((int)$st->fetchColumn() > 0);
    }
    if (!$ok) {
        http_response_code(403);
        echo "禁止匿名执行。请用命令行: /www/server/php/85/bin/php tools/install_video.php\n";
        exit;
    }
}

$pdo = db();
$n = 0;

/** 该表当前有哪些列 */
function video_cols(PDO $pdo, string $table): array
{
    try { return $pdo->query('SHOW COLUMNS FROM `' . $table . '`')->fetchAll(PDO::FETCH_COLUMN) ?: []; }
    catch (Exception $e) { return []; }
}

// 5 个视频列: 列名 => [类型定义, 注释]
$videoDefs = [
    'video'          => ["VARCHAR(255) NOT NULL DEFAULT ''", '视频URL'],
    'video_w'        => ['INT UNSIGNED NOT NULL DEFAULT 0', '视频宽'],
    'video_h'        => ['INT UNSIGNED NOT NULL DEFAULT 0', '视频高'],
    'video_duration' => ['INT NOT NULL DEFAULT 0', '视频时长(秒)'],
    'video_size'     => ['BIGINT NOT NULL DEFAULT 0', '视频字节数'],
];

foreach (['social_messages', 'social_pm_messages'] as $table) {
    echo "[表] $table\n";
    $have = video_cols($pdo, $table);
    if (!$have) { echo "  ⚠️  取不到 $table 的列, 跳过\n"; continue; }
    $after = 'image_h';
    foreach ($videoDefs as $col => $def) {
        if (in_array($col, $have, true)) { echo "  ⏭  $col 已存在\n"; continue; }
        try {
            $pdo->exec("ALTER TABLE `$table` ADD COLUMN `$col` {$def[0]} COMMENT '{$def[1]}' AFTER `$after`");
            echo "  ✅ 新增 $col\n"; $n++;
            $have[] = $col; $after = $col;
        } catch (Exception $e) { echo "  ❌ $col 失败: " . $e->getMessage() . "\n"; }
    }
    // 私聊消息表原本没有 msg_type (群消息表早就有), 补上以便统一按消息类型渲染
    if (!in_array('msg_type', $have, true)) {
        try {
            $pdo->exec("ALTER TABLE `$table` ADD COLUMN `msg_type` VARCHAR(16) NOT NULL DEFAULT '' COMMENT '消息类型: video=视频' AFTER `video_size`");
            echo "  ✅ 新增 msg_type\n"; $n++;
        } catch (Exception $e) { echo "  ❌ msg_type 失败: " . $e->getMessage() . "\n"; }
    } else {
        echo "  ⏭  msg_type 已存在\n";
    }
}

// ---------- settings.video_config ----------
echo "[配置] settings.video_config\n";
try {
    $defaults = [
        'enabled'        => 1,
        'max_mb'         => 100,
        'total_limit_mb' => 600,
        'keep_days'      => 0,
        'auto_clean'     => 1,
    ];
    $st = $pdo->prepare('SELECT `value` FROM `settings` WHERE `key` = ?');
    $st->execute(['video_config']);
    $raw = (string)($st->fetchColumn() ?: '');
    if ($raw === '') {
        $json = json_encode($defaults, JSON_UNESCAPED_UNICODE);
        $pdo->prepare('INSERT INTO `settings` (`key`, `value`) VALUES (?, ?)')->execute(['video_config', $json]);
        echo "  ✅ 已写入默认值: $json\n"; $n++;
    } else {
        $cfg = json_decode($raw, true);
        $miss = [];
        if (is_array($cfg)) {
            foreach ($defaults as $k => $v) { if (!array_key_exists($k, $cfg)) $miss[] = $k; }
        }
        echo "  ⏭  已存在, 原值不动: $raw\n";
        if ($miss) echo '  ⚠️  缺少键: ' . implode(', ', $miss) . " (api.php 会用默认值兜底)\n";
    }
} catch (Exception $e) { echo '  ❌ 失败: ' . $e->getMessage() . "\n"; }

// ---------- 结果核对 ----------
echo "\n[核对] 迁移后列:\n";
foreach (['social_messages', 'social_pm_messages'] as $table) {
    $cols = video_cols($pdo, $table);
    echo '  ' . $table . ': ' . implode(', ', array_slice($cols, -7)) . "\n";
}
echo "\n完成, 共执行 $n 项\n";
