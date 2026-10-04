<?php
/**
 * 用户标签功能: users.tags (逗号分隔, 最多 5 个, 每个 <= 10 字)
 * 幂等: 重复执行安全
 */
require_once __DIR__ . '/../config.php';
$db = db();
$out = [];
$col = $db->query("SHOW COLUMNS FROM users LIKE 'tags'")->fetch();
if (!$col) {
    $db->exec("ALTER TABLE users ADD COLUMN tags VARCHAR(255) NOT NULL DEFAULT '' COMMENT '管理员自定义标签, 逗号分隔'");
    $out[] = '[列] users.tags 已新增';
} else {
    $out[] = '[列] users.tags 已存在';
}
$st = $db->prepare('SELECT `value` FROM settings WHERE `key` = ?');
$st->execute(['user_tag_presets']);
if ($st->fetchColumn() === false) {
    $db->prepare('INSERT INTO settings (`key`, `value`) VALUES (?, ?)')
       ->execute(['user_tag_presets', json_encode(['官方', '已核实', '骗子', '风险用户'], JSON_UNESCAPED_UNICODE)]);
    $out[] = '[配置] user_tag_presets 已初始化';
} else {
    $out[] = '[配置] user_tag_presets 已存在';
}
foreach ($out as $l) echo $l . PHP_EOL;
echo 'DONE' . PHP_EOL;
