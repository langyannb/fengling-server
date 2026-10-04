<?php
/**
 * 抽奖活动重置: users.lottery_day_reset_at (当天计数单独清零用)
 * 幂等: 重复执行安全
 */
require_once __DIR__ . '/../config.php';
$db = db();
$out = [];
$col = $db->query("SHOW COLUMNS FROM users LIKE 'lottery_day_reset_at'")->fetch();
if (!$col) {
    $db->exec("ALTER TABLE users ADD COLUMN lottery_day_reset_at DATETIME NULL COMMENT '当天抽奖计数清零时间, 之前的记录不算今天的次数'");
    $out[] = '[列] users.lottery_day_reset_at 已新增';
} else {
    $out[] = '[列] users.lottery_day_reset_at 已存在';
}
$col2 = $db->query("SHOW COLUMNS FROM users LIKE 'lottery_reset_at'")->fetch();
$out[] = $col2 ? '[列] users.lottery_reset_at 已存在' : '[警告] users.lottery_reset_at 缺失, 请先跑 install_lottery.php';
foreach ($out as $l) echo $l . PHP_EOL;
echo 'DONE' . PHP_EOL;
