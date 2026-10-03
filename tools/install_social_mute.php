<?php
/**
 * 社交系统: 消息免打扰表
 *   social_mutes  某个用户对某个群开了免打扰 (有行 = 免打扰)
 * 幂等: 可重复执行。
 */
require_once __DIR__ . '/../config.php';

echo "== 安装 social_mutes ==\n";

try {
    db()->exec("CREATE TABLE IF NOT EXISTS social_mutes (
        id INT UNSIGNED NOT NULL AUTO_INCREMENT,
        group_id INT UNSIGNED NOT NULL,
        user_id INT UNSIGNED NOT NULL,
        created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
        PRIMARY KEY (id),
        UNIQUE KEY uk_group_user (group_id, user_id),
        KEY idx_user (user_id)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4");
    echo "[1/2] social_mutes 表 OK\n";

    $cols = [];
    foreach (db()->query('SHOW COLUMNS FROM social_mutes')->fetchAll(PDO::FETCH_ASSOC) as $c) {
        $cols[] = $c['Field'];
    }
    echo "[2/2] 列: " . implode(', ', $cols) . "\n";
    echo "== 完成 ==\n";
} catch (Exception $e) {
    echo "失败: " . $e->getMessage() . "\n";
    exit(1);
}
