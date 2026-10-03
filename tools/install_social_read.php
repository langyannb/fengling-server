<?php
/**
 * 群聊已读位置表 (幂等迁移)
 * social_reads: 记录每个用户在每个群读到哪一条消息, 用于群列表未读角标与进群定位第一条未读
 */
require_once __DIR__ . '/../config.php';

$pdo = db();
echo "== social_reads 迁移 ==\n";
$pdo->exec("CREATE TABLE IF NOT EXISTS social_reads (
    id INT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id INT UNSIGNED NOT NULL,
    group_id INT UNSIGNED NOT NULL,
    last_read_id INT UNSIGNED NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_user_group (user_id, group_id),
    KEY idx_group (group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4");

$has = false;
foreach ($pdo->query('SHOW TABLES')->fetchAll(PDO::FETCH_COLUMN) as $t) {
    if ($t === 'social_reads') $has = true;
}
echo $has ? "[1/1] social_reads 就绪\n" : "[1/1] 建表失败\n";
echo "== 完成 ==\n";
