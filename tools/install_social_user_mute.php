<?php
/**
 * 群内禁言表迁移 (幂等)
 */
require_once __DIR__ . '/../config.php';

$pdo = db();
$pdo->exec("CREATE TABLE IF NOT EXISTS social_user_mutes (
    id INT AUTO_INCREMENT PRIMARY KEY,
    group_id INT NOT NULL DEFAULT 0 COMMENT '0 = 全站禁言',
    user_id INT NOT NULL,
    until_at DATETIME NULL COMMENT 'NULL = 永久',
    reason VARCHAR(120) NOT NULL DEFAULT '',
    created_by INT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_group_user (group_id, user_id),
    KEY idx_user (user_id),
    KEY idx_until (until_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4");
echo "[1/1] social_user_mutes 表就绪\n";
$n = $pdo->query('SELECT COUNT(*) FROM social_user_mutes')->fetchColumn();
echo "DONE (当前禁言记录 $n 条)\n";
