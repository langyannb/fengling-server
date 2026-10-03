<?php
/**
 * 社交系统数据库迁移 (幂等, 可重复执行)
 * 用法: 放到站点根目录下的 tools/ 里, 然后 cd 站点根目录 && php tools/install_social.php
 */
require_once __DIR__ . '/../config.php';

$pdo = db();

$stmts = [
    'social_groups' => "CREATE TABLE IF NOT EXISTS `social_groups` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `name` VARCHAR(50) NOT NULL UNIQUE,
      `icon` VARCHAR(255) DEFAULT '',
      `description` VARCHAR(255) DEFAULT '',
      `notice` TEXT,
      `sort_order` INT DEFAULT 0,
      `is_active` TINYINT(1) DEFAULT 1,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
    'social_messages' => "CREATE TABLE IF NOT EXISTS `social_messages` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `group_id` INT UNSIGNED NOT NULL,
      `user_id` INT UNSIGNED NOT NULL,
      `content` TEXT,
      `at_users` VARCHAR(255) DEFAULT '',
      `is_recalled` TINYINT(1) DEFAULT 0,
      `recalled_by` INT UNSIGNED DEFAULT 0,
      `recalled_at` DATETIME DEFAULT NULL,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      KEY `idx_group_msg` (`group_id`, `id`),
      KEY `idx_user` (`user_id`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
    'notifications' => "CREATE TABLE IF NOT EXISTS `notifications` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `user_id` INT UNSIGNED NOT NULL,
      `title` VARCHAR(100) DEFAULT '',
      `content` TEXT,
      `type` VARCHAR(20) DEFAULT 'system',
      `link` VARCHAR(500) DEFAULT '',
      `is_read` TINYINT(1) DEFAULT 0,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      KEY `idx_user_read` (`user_id`, `is_read`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
    'captcha_codes' => "CREATE TABLE IF NOT EXISTS `captcha_codes` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `token` VARCHAR(64) NOT NULL UNIQUE,
      `code` VARCHAR(10) NOT NULL,
      `ip` VARCHAR(45) DEFAULT '',
      `used` TINYINT(1) DEFAULT 0,
      `expires_at` DATETIME NOT NULL,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
];

$n = 0;
foreach ($stmts as $name => $sql) {
    try {
        $pdo->exec($sql);
        echo '[表] ' . $name . " OK\n";
        $n++;
    } catch (Exception $e) {
        echo '[表] ' . $name . ' 失败: ' . $e->getMessage() . "\n";
    }
}

// 预置群组
$presets = [
    ['Aevum', 'Aevum 官方交流群', '欢迎来到 Aevum 交流群，请文明发言，禁止刷屏与广告。', 1],
    ['宇宙', '宇宙 官方交流群', '欢迎来到宇宙交流群，请文明发言，禁止刷屏与广告。', 2],
];
foreach ($presets as $p) {
    $st = $pdo->prepare('SELECT id FROM social_groups WHERE name = ?');
    $st->execute([$p[0]]);
    if ($st->fetch()) {
        echo '[预置] 群组 ' . $p[0] . " 已存在, 跳过\n";
        continue;
    }
    $pdo->prepare('INSERT INTO social_groups (name, description, notice, sort_order, is_active) VALUES (?, ?, ?, ?, 1)')
        ->execute($p);
    echo '[预置] 群组 ' . $p[0] . " 已创建\n";
}

echo "---- 表结构 ----\n";
foreach (array_keys($stmts) as $t) {
    $cols = $pdo->query('SHOW COLUMNS FROM `' . $t . '`')->fetchAll(PDO::FETCH_COLUMN);
    echo $t . ': ' . implode(', ', $cols) . "\n";
}
echo "---- 群组 ----\n";
foreach ($pdo->query('SELECT id, name, is_active FROM social_groups ORDER BY sort_order, id')->fetchAll(PDO::FETCH_ASSOC) as $g) {
    echo '  #' . $g['id'] . ' ' . $g['name'] . ' (active=' . $g['is_active'] . ")\n";
}
echo "迁移完成\n";
