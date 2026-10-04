<?php
/**
 * 抽奖系统数据库迁移 (幂等, 可重复执行)
 *   - lottery_prizes  奖项(名称/卡密类型/描述/排序/启用)
 *   - lottery_codes   卡密池(所属奖项/卡密/使用者/使用时间)
 *   - lottery_draws   中奖记录
 *   - users.lottery_quota  用户单独抽奖次数 (-1 = 跟随活动默认)
 *   - settings.lottery_config  活动配置 JSON
 */
require_once __DIR__ . '/../config.php';

$pdo = db();

$stmts = [
    'lottery_prizes' => "CREATE TABLE IF NOT EXISTS `lottery_prizes` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `name` VARCHAR(50) NOT NULL,
      `card_type` VARCHAR(20) NOT NULL DEFAULT '通用',
      `description` VARCHAR(255) DEFAULT '',
      `sort_order` INT DEFAULT 0,
      `is_active` TINYINT(1) DEFAULT 1,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      UNIQUE KEY `uk_name` (`name`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
    'lottery_codes' => "CREATE TABLE IF NOT EXISTS `lottery_codes` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `prize_id` INT UNSIGNED NOT NULL,
      `code` VARCHAR(64) NOT NULL,
      `user_id` INT UNSIGNED DEFAULT 0,
      `used_at` DATETIME DEFAULT NULL,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      UNIQUE KEY `uk_code` (`code`),
      KEY `idx_prize_used` (`prize_id`, `user_id`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
    'lottery_draws' => "CREATE TABLE IF NOT EXISTS `lottery_draws` (
      `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
      `user_id` INT UNSIGNED NOT NULL,
      `prize_id` INT UNSIGNED NOT NULL,
      `code_id` INT UNSIGNED DEFAULT 0,
      `code` VARCHAR(64) NOT NULL,
      `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      KEY `idx_user` (`user_id`),
      KEY `idx_prize` (`prize_id`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
];

$n = 0;
foreach ($stmts as $name => $sql) {
    try { $pdo->exec($sql); echo '[表] ' . $name . " OK\n"; $n++; }
    catch (Exception $e) { echo '[表] ' . $name . ' 失败: ' . $e->getMessage() . "\n"; }
}

// users.lottery_quota
try {
    $cols = $pdo->query('SHOW COLUMNS FROM `users`')->fetchAll(PDO::FETCH_COLUMN);
    if (!in_array('lottery_quota', $cols, true)) {
        $pdo->exec('ALTER TABLE `users` ADD COLUMN `lottery_quota` INT NOT NULL DEFAULT -1 COMMENT "单独抽奖次数, -1=跟随活动默认"');
        echo "[列] users.lottery_quota OK\n"; $n++;
    } else {
        echo "[列] users.lottery_quota 已存在\n";
    }
} catch (Exception $e) { echo '[列] users.lottery_quota 失败: ' . $e->getMessage() . "\n"; }

// settings.lottery_config
try {
    $exists = $pdo->query("SELECT COUNT(*) FROM `settings` WHERE `key` = 'lottery_config'")->fetchColumn();
    if ((int)$exists === 0) {
        $cfg = json_encode([
            'enabled' => 0,
            'title' => '免费抽卡密',
            'content' => "关注风铃分享库，天天有机会抽到天卡 / 周卡 / 月卡卡密！\n抽到的卡密会同时保存在「我的 → 消息中心」，可随时查看复制。",
            'per_user_limit' => 1,
        ], JSON_UNESCAPED_UNICODE);
        $st = $pdo->prepare('INSERT INTO `settings` (`key`, `value`) VALUES (?, ?)');
        $st->execute(['lottery_config', $cfg]);
        echo "[配置] settings.lottery_config OK\n"; $n++;
    } else {
        echo "[配置] settings.lottery_config 已存在\n";
    }
} catch (Exception $e) { echo '[配置] settings.lottery_config 失败: ' . $e->getMessage() . "\n"; }

echo "\n完成, 共执行 $n 项\n";
