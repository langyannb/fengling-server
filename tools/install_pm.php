<?php
/**
 * 私聊 + 用户主页 + 抽奖每日/重置 数据库迁移 (幂等, 可重复执行)
 *   - social_pms          私聊会话 (两个用户一条, user_a < user_b)
 *   - social_pm_messages  私聊消息
 *   - social_pm_reads     私聊已读位置
 *   - users.lottery_reset_at  抽奖次数重置时间点 (计数只看此时间之后的中奖记录)
 */
require_once __DIR__ . '/../config.php';

$pdo = db();

$stmts = [
    'social_pms' => "CREATE TABLE IF NOT EXISTS `social_pms` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `user_a` INT UNSIGNED NOT NULL,
      `user_b` INT UNSIGNED NOT NULL,
      `last_message_id` BIGINT UNSIGNED DEFAULT 0,
      `last_at` DATETIME DEFAULT NULL,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      UNIQUE KEY `uk_pair` (`user_a`, `user_b`),
      KEY `idx_b` (`user_b`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
    'social_pm_messages' => "CREATE TABLE IF NOT EXISTS `social_pm_messages` (
      `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `conv_id` INT UNSIGNED NOT NULL,
      `from_user` INT UNSIGNED NOT NULL,
      `to_user` INT UNSIGNED NOT NULL,
      `content` TEXT,
      `image` VARCHAR(255) DEFAULT '',
      `image_w` INT UNSIGNED DEFAULT 0,
      `image_h` INT UNSIGNED DEFAULT 0,
      `is_recalled` TINYINT(1) DEFAULT 0,
      `recalled_by` INT UNSIGNED DEFAULT 0,
      `recalled_at` DATETIME DEFAULT NULL,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      KEY `idx_conv` (`conv_id`, `id`),
      KEY `idx_to` (`to_user`, `id`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
    'social_pm_reads' => "CREATE TABLE IF NOT EXISTS `social_pm_reads` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `user_id` INT UNSIGNED NOT NULL,
      `conv_id` INT UNSIGNED NOT NULL,
      `last_read_id` BIGINT UNSIGNED DEFAULT 0,
      `updated_at` DATETIME DEFAULT NULL,
      UNIQUE KEY `uk_user_conv` (`user_id`, `conv_id`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
];

$n = 0;
foreach ($stmts as $name => $sql) {
    try { $pdo->exec($sql); echo '[表] ' . $name . " OK\n"; $n++; }
    catch (Exception $e) { echo '[表] ' . $name . ' 失败: ' . $e->getMessage() . "\n"; }
}

// users.lottery_reset_at
try {
    $cols = $pdo->query('SHOW COLUMNS FROM `users`')->fetchAll(PDO::FETCH_COLUMN);
    if (!in_array('lottery_reset_at', $cols, true)) {
        $pdo->exec('ALTER TABLE `users` ADD COLUMN `lottery_reset_at` DATETIME DEFAULT NULL COMMENT "抽奖次数重置时间, 计数只看此时间之后"');
        echo "[列] users.lottery_reset_at OK\n"; $n++;
    } else {
        echo "[列] users.lottery_reset_at 已存在\n";
    }
} catch (Exception $e) { echo '[列] users.lottery_reset_at 失败: ' . $e->getMessage() . "\n"; }

// settings.lottery_config 补上 daily_limit (每天可抽次数, 0 = 不限)
try {
    $raw = (string)$pdo->query("SELECT `value` FROM `settings` WHERE `key` = 'lottery_config'")->fetchColumn();
    $cfg = $raw !== '' ? json_decode($raw, true) : null;
    if (!is_array($cfg)) $cfg = [];
    if (!array_key_exists('daily_limit', $cfg)) {
        $cfg['daily_limit'] = 0;
        $json = json_encode($cfg, JSON_UNESCAPED_UNICODE);
        $st = $pdo->prepare('INSERT INTO `settings` (`key`, `value`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)');
        $st->execute(['lottery_config', $json]);
        echo "[配置] lottery_config.daily_limit = 0 OK\n"; $n++;
    } else {
        echo "[配置] lottery_config.daily_limit 已存在\n";
    }
} catch (Exception $e) { echo '[配置] lottery_config.daily_limit 失败: ' . $e->getMessage() . "\n"; }

echo "\n完成, 共执行 $n 项\n";
