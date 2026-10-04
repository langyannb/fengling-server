<?php
/**
 * 群全体禁言: social_groups.all_muted TINYINT(1) NOT NULL DEFAULT 0
 *   all_muted = 1 时, 该群除管理员外都不能发言
 * 幂等: 重复执行安全 (先查 information_schema, 已存在则跳过 ALTER)
 */
require_once __DIR__ . '/../config.php';

$db = db();
$out = [];

$st = $db->prepare("SELECT COLUMN_NAME FROM information_schema.COLUMNS
                     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'social_groups' AND COLUMN_NAME = 'all_muted'");
$st->execute();
if (!$st->fetch()) {
    $db->exec("ALTER TABLE social_groups ADD COLUMN all_muted TINYINT(1) NOT NULL DEFAULT 0 COMMENT '1=开启全体禁言(仅管理员可发言)'");
    $out[] = '[列] social_groups.all_muted 已新增';
} else {
    $out[] = '[列] social_groups.all_muted 已存在, 跳过';
}

$row = $db->query("SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
                    FROM information_schema.COLUMNS
                   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'social_groups' AND COLUMN_NAME = 'all_muted'")
          ->fetch(PDO::FETCH_ASSOC);
$out[] = '[校验] ' . json_encode($row, JSON_UNESCAPED_UNICODE);
$out[] = '[统计] 已开启全体禁言的群: ' . (int)$db->query('SELECT COUNT(*) FROM social_groups WHERE all_muted = 1')->fetchColumn();

foreach ($out as $l) echo $l . PHP_EOL;
echo 'DONE' . PHP_EOL;
