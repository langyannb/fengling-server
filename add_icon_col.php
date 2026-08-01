<?php
// 添加 categories.icon 列 (MySQL 5.7 不支持 ADD COLUMN IF NOT EXISTS)
$pdo = new PDO('mysql:host=localhost;dbname=flfxk;charset=utf8mb4', 'flfxk', 'REDACTED_DB_PASS');
$check = $pdo->query("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='flfxk' AND TABLE_NAME='categories' AND COLUMN_NAME='icon'")->fetchColumn();
if ($check == 0) {
    $pdo->exec("ALTER TABLE categories ADD COLUMN icon VARCHAR(255) DEFAULT '' COMMENT '分类图标URL'");
    echo "ADDED\n";
} else {
    echo "EXISTS\n";
}
