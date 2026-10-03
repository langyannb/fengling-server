<?php
/**
 * 社交消息「引用回复」字段迁移 (幂等)
 *
 * 给 social_messages 增加 quote_id (被引用的消息 id, 0 = 不是引用)
 */
require_once __DIR__ . '/../config.php';

$pdo = db();
$cols = [];
foreach ($pdo->query('SHOW COLUMNS FROM social_messages') as $c) { $cols[$c['Field']] = true; }

if (isset($cols['quote_id'])) {
    echo "[1/1] social_messages.quote_id 已存在, 跳过\n";
} else {
    $pdo->exec('ALTER TABLE social_messages ADD COLUMN quote_id INT NOT NULL DEFAULT 0 AFTER at_users');
    echo "[1/1] social_messages 增加 quote_id\n";
}
echo "DONE\n";
