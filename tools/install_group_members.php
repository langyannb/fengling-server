<?php
/**
 * 群成员体系迁移 (幂等, 可重复执行, 结果一致)
 *  1) 建表 social_group_members (群成员)
 *  2) social_messages 加列 msg_type (''/text = 普通消息, system = 系统消息)
 *  3) 回填: 所有现有 users -> 所有现有 social_groups (INSERT IGNORE), 防止老用户被锁在群外
 *
 * 用法: cd 站点根目录 && php tools/install_group_members.php
 * 关键: 回填用 INSERT IGNORE + 唯一键 uk_group_user, 跑两次不会重复插, 也不会覆盖已有 role/joined_at
 */
require_once __DIR__ . '/../config.php';

$db  = db();
$log = [];

function col_exists(PDO $db, string $table, string $col): bool
{
    $st = $db->prepare("SELECT COLUMN_NAME FROM information_schema.COLUMNS
                         WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?");
    $st->execute([$table, $col]);
    return (bool)$st->fetchColumn();
}

function table_exists(PDO $db, string $table): bool
{
    $st = $db->prepare("SELECT TABLE_NAME FROM information_schema.TABLES
                         WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?");
    $st->execute([$table]);
    return (bool)$st->fetchColumn();
}

// ---------- 1. 建表 social_group_members ----------
if (table_exists($db, 'social_group_members')) {
    $log[] = '[表] social_group_members 已存在, 跳过 CREATE';
} else {
    $db->exec("CREATE TABLE IF NOT EXISTS `social_group_members` (
      `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
      `group_id` INT UNSIGNED NOT NULL,
      `user_id` INT UNSIGNED NOT NULL,
      `role` VARCHAR(16) NOT NULL DEFAULT 'member' COMMENT 'owner/admin/member',
      `joined_at` DATETIME NOT NULL,
      PRIMARY KEY (`id`),
      UNIQUE KEY `uk_group_user` (`group_id`, `user_id`),
      KEY `idx_user` (`user_id`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4");
    $log[] = '[表] social_group_members 已创建 OK';
}
$cnt = (int)$db->query('SELECT COUNT(*) FROM social_group_members')->fetchColumn();
$log[] = '[表] social_group_members 当前行数: ' . $cnt . ' OK';

// ---------- 2. social_messages.msg_type ----------
if (col_exists($db, 'social_messages', 'msg_type')) {
    $log[] = '[列] social_messages.msg_type 已存在, 跳过 ALTER';
} else {
    $db->exec("ALTER TABLE `social_messages` ADD COLUMN `msg_type` VARCHAR(16) NOT NULL DEFAULT '' COMMENT '空/text=普通消息, system=系统消息'");
    $log[] = '[列] social_messages.msg_type 已新增 OK';
}
if (col_exists($db, 'social_messages', 'msg_type')) {
    $log[] = '[列] social_messages.msg_type 校验 OK';
} else {
    $log[] = '[列] social_messages.msg_type 校验失败 !!';
    foreach ($log as $l) echo $l . PHP_EOL;
    exit(1);
}

// ---------- 3. 回填: 所有 users x 所有 social_groups ----------
$userTotal = (int)$db->query('SELECT COUNT(*) FROM users')->fetchColumn();
$grpTotal  = (int)$db->query('SELECT COUNT(*) FROM social_groups')->fetchColumn();
$before    = (int)$db->query('SELECT COUNT(*) FROM social_group_members')->fetchColumn();
$log[] = '[回填] 现有 users=' . $userTotal . ', social_groups=' . $grpTotal
       . ', 期望笛卡尔积=' . ($userTotal * $grpTotal) . ', 回填前成员行=' . $before;

$ins = $db->exec("INSERT IGNORE INTO `social_group_members` (`group_id`, `user_id`, `role`, `joined_at`)
                  SELECT g.`id`, u.`id`, IF(u.`role` = 'admin', 'owner', 'member'), NOW()
                    FROM `social_groups` g CROSS JOIN `users` u");
$after = (int)$db->query('SELECT COUNT(*) FROM social_group_members')->fetchColumn();
$log[] = '[回填] 本次新插入 ' . (int)$ins . ' 行, 回填后成员行=' . $after . ' OK';

// 漏网检查: 还有哪个 (群, 用户) 没有成员行
$missing = (int)$db->query("SELECT COUNT(*) FROM (SELECT g.id AS gid, u.id AS uid FROM social_groups g CROSS JOIN users u) t
                             LEFT JOIN social_group_members m ON m.group_id = t.gid AND m.user_id = t.uid
                             WHERE m.id IS NULL")->fetchColumn();
$log[] = '[校验] 缺失成员行(应为 0): ' . $missing;
if ($missing !== 0) { foreach ($log as $l) echo $l . PHP_EOL; exit(1); }

// ---------- 4. 结构与统计 ----------
$log[] = '[统计] 角色分布: ' . json_encode(
    $db->query('SELECT role, COUNT(*) AS c FROM social_group_members GROUP BY role ORDER BY role')
       ->fetchAll(PDO::FETCH_KEY_PAIR), JSON_UNESCAPED_UNICODE);
$log[] = '[结构] social_group_members: ' . implode(', ',
    $db->query('SHOW COLUMNS FROM social_group_members')->fetchAll(PDO::FETCH_COLUMN));
$log[] = '[结构] social_messages: ' . implode(', ',
    $db->query('SHOW COLUMNS FROM social_messages')->fetchAll(PDO::FETCH_COLUMN));

foreach ($db->query('SELECT g.id, g.name, COUNT(m.id) AS c FROM social_groups g
                      LEFT JOIN social_group_members m ON m.group_id = g.id
                     GROUP BY g.id, g.name ORDER BY g.id')->fetchAll() as $r) {
    $log[] = '[群] #' . $r['id'] . ' ' . $r['name'] . ' 成员数=' . $r['c'];
}

foreach ($log as $l) echo $l . PHP_EOL;
echo 'DONE' . PHP_EOL;
