<?php
/**
 * 抽奖「一堆设置」迁移 (幂等, 可重复执行)
 *   - lottery_prizes 增加 weight 列 (加权抽奖权重, 默认 100; 已存在则跳过)
 *   - settings.lottery_config 补齐契约 A 节的新键 (已有值一律不动)
 * 用法: /www/server/php/85/bin/php tools/install_lottery_window.php
 */
require_once __DIR__ . '/../config.php';

$pdo = db();
$n = 0;

// ---------- 1. lottery_prizes.weight ----------
try {
    $cols = $pdo->query('SHOW COLUMNS FROM `lottery_prizes`')->fetchAll(PDO::FETCH_COLUMN);
    if (is_array($cols) && $cols && !in_array('weight', $cols, true)) {
        $pdo->exec('ALTER TABLE `lottery_prizes` ADD COLUMN `weight` INT NOT NULL DEFAULT 100 COMMENT "抽奖权重, 越大越容易抽到, 0=不参与"');
        echo "[列] lottery_prizes.weight OK\n"; $n++;
    } else {
        echo "[列] lottery_prizes.weight 已存在\n";
    }
} catch (Exception $e) { echo '[列] lottery_prizes.weight 失败: ' . $e->getMessage() . "\n"; }

// 已有的 NULL 权重兜底成 100 (不会覆盖任何非 NULL 值)
try {
    $fixed = $pdo->exec('UPDATE `lottery_prizes` SET `weight` = 100 WHERE `weight` IS NULL');
    echo "[数据] weight 为 NULL 的行改成 100: " . (int)$fixed . " 行\n";
} catch (Exception $e) { echo '[数据] weight 兜底失败: ' . $e->getMessage() . "\n"; }

// ---------- 2. settings.lottery_config 补新键 ----------
$defaults = [
    'enabled'           => 0,
    'title'             => '免费抽卡密',
    'content'           => '',
    'success_text'      => '',
    'empty_text'        => '奖品已抽完, 请稍后再来',
    'per_user_limit'    => 1,
    'daily_limit'       => 0,
    'week_limit'        => 0,
    'daily_reset_time'  => '00:00',
    'cooldown_seconds'  => 0,
    'daily_total_limit' => 0,
    'windows_enabled'   => 0,
    'windows'           => [],
    'start_date'        => '',
    'end_date'          => '',
    'show_prizes'       => 1,
    'show_stock'        => 1,
    'notify_winner'     => 1,
];

try {
    $st = $pdo->prepare('SELECT `value` FROM `settings` WHERE `key` = ?');
    $st->execute(['lottery_config']);
    $raw = (string)($st->fetchColumn() ?: '');
    $cfg = $raw !== '' ? json_decode($raw, true) : null;
    if (!is_array($cfg)) $cfg = [];
    $added = [];
    foreach ($defaults as $k => $v) {
        if (!array_key_exists($k, $cfg)) { $cfg[$k] = $v; $added[] = $k; }
    }
    if (!$raw) {
        $json = json_encode($cfg, JSON_UNESCAPED_UNICODE);
        $pdo->prepare('INSERT INTO `settings` (`key`, `value`) VALUES (?, ?)')->execute(['lottery_config', $json]);
        echo "[配置] settings.lottery_config 新建并写入默认值 (" . count($cfg) . " 个键)\n"; $n++;
    } elseif ($added) {
        $json = json_encode($cfg, JSON_UNESCAPED_UNICODE);
        $pdo->prepare('UPDATE `settings` SET `value` = ? WHERE `key` = ?')->execute([$json, 'lottery_config']);
        echo '[配置] settings.lottery_config 补齐新键: ' . implode(', ', $added) . "\n"; $n++;
    } else {
        echo "[配置] settings.lottery_config 新键已齐全, 跳过\n";
    }
} catch (Exception $e) { echo '[配置] settings.lottery_config 失败: ' . $e->getMessage() . "\n"; }

echo "\n完成, 共执行 $n 项\n";
