<?php
/**
 * 群聊图片消息迁移 (幂等, 可重复执行)
 * 用法: 放到站点根目录下的 tools/ 里, 然后 cd 站点根目录 && php tools/install_social_image.php
 * 作用: 给 social_messages 增加 image / image_w / image_h 三列
 */
require_once __DIR__ . '/../config.php';

$pdo = db();
$cols = $pdo->query('SHOW COLUMNS FROM social_messages')->fetchAll(PDO::FETCH_COLUMN);
$add = [
    'image'   => "ALTER TABLE `social_messages` ADD COLUMN `image` VARCHAR(255) NOT NULL DEFAULT '' COMMENT '图片地址(对象存储)'",
    'image_w' => "ALTER TABLE `social_messages` ADD COLUMN `image_w` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '原图宽'",
    'image_h' => "ALTER TABLE `social_messages` ADD COLUMN `image_h` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '原图高'",
];
$i = 0;
foreach ($add as $name => $sql) {
    $i++;
    if (in_array($name, $cols, true)) { echo "[$i/" . count($add) . "] $name 已存在, 跳过\n"; continue; }
    $pdo->exec($sql);
    echo "[$i/" . count($add) . "] $name 添加成功\n";
}
echo "--- social_messages 当前列 ---\n";
foreach ($pdo->query('SHOW COLUMNS FROM social_messages')->fetchAll(PDO::FETCH_ASSOC) as $c) {
    echo '  ' . $c['Field'] . '  ' . $c['Type'] . "\n";
}
echo "完成\n";
