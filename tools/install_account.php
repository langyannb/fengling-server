<?php
// 账号系统数据库迁移 (幂等, 可反复执行)
// 用法: php tools/install_account.php
require_once __DIR__ . '/../config.php';

$pdo = new PDO(
    'mysql:host=' . DB_HOST . ';port=' . DB_PORT . ';dbname=' . DB_NAME . ';charset=' . DB_CHARSET,
    DB_USER, DB_PASS,
    [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION]
);

function col_exists(PDO $pdo, string $table, string $col): bool {
    $s = $pdo->prepare('SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?');
    $s->execute([$table, $col]);
    return (int)$s->fetchColumn() > 0;
}
function idx_exists(PDO $pdo, string $table, string $idx): bool {
    $s = $pdo->prepare('SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?');
    $s->execute([$table, $idx]);
    return (int)$s->fetchColumn() > 0;
}
function run(PDO $pdo, string $sql, string $label): void {
    try { $pdo->exec($sql); echo "  ✅ $label\n"; }
    catch (Exception $e) { echo "  ⚠️  $label -> " . $e->getMessage() . "\n"; }
}

echo "[1/4] users 表增列\n";
$cols = [
    'email'          => "ALTER TABLE `users` ADD COLUMN `email` VARCHAR(120) NULL DEFAULT NULL COMMENT '邮箱' AFTER `nickname`",
    'email_verified' => "ALTER TABLE `users` ADD COLUMN `email_verified` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '邮箱已验证' AFTER `email`",
    'avatar'         => "ALTER TABLE `users` ADD COLUMN `avatar` VARCHAR(255) NOT NULL DEFAULT '' COMMENT '头像URL' AFTER `email_verified`",
    'bio'            => "ALTER TABLE `users` ADD COLUMN `bio` VARCHAR(255) NOT NULL DEFAULT '' COMMENT '简介' AFTER `avatar`",
    'last_login_at'  => "ALTER TABLE `users` ADD COLUMN `last_login_at` TIMESTAMP NULL DEFAULT NULL COMMENT '最后登录' AFTER `created_at`",
];
foreach ($cols as $col => $sql) {
    if (col_exists($pdo, 'users', $col)) { echo "  ⏭  users.$col 已存在\n"; continue; }
    run($pdo, $sql, "users.$col");
}

echo "[2/4] users.role 默认值改 user\n";
run($pdo, "ALTER TABLE `users` MODIFY COLUMN `role` VARCHAR(20) NOT NULL DEFAULT 'user' COMMENT '角色 user/admin'", 'role 默认 user');

echo "[3/4] users 唯一索引\n";
if (idx_exists($pdo, 'users', 'uk_username')) { echo "  \u23ed  uk_username 已存在\n"; }
else { run($pdo, "ALTER TABLE `users` ADD UNIQUE KEY `uk_username` (`username`)", 'uk_username'); }

if (idx_exists($pdo, 'users', 'uk_email')) { echo "  ⏭  uk_email 已存在\n"; }
else { run($pdo, "ALTER TABLE `users` ADD UNIQUE KEY `uk_email` (`email`)", 'uk_email'); }

echo "[4/4] 邮箱验证码表 email_codes\n";
run($pdo, "CREATE TABLE IF NOT EXISTS `email_codes` (
  `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  `email` VARCHAR(120) NOT NULL,
  `code` VARCHAR(10) NOT NULL,
  `purpose` VARCHAR(20) NOT NULL DEFAULT 'register',
  `tries` INT NOT NULL DEFAULT 0,
  `used` TINYINT(1) NOT NULL DEFAULT 0,
  `ip` VARCHAR(64) DEFAULT '',
  `expires_at` DATETIME NOT NULL,
  `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  KEY `idx_email_purpose` (`email`, `purpose`),
  KEY `idx_ip_created` (`ip`, `created_at`),
  KEY `idx_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='邮箱验证码'", 'email_codes');

echo "\n完成。users 表当前列: ";
$colsNow = $pdo->query('SHOW COLUMNS FROM users')->fetchAll(PDO::FETCH_COLUMN);
echo implode(', ', $colsNow) . "\n";
