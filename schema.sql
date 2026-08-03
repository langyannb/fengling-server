-- 风铃分享库 (flfxk) 数据库表结构
-- 用户表
CREATE TABLE IF NOT EXISTS `users` (
  `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  `username` VARCHAR(50) NOT NULL UNIQUE,
  `password` VARCHAR(255) NOT NULL,
  `nickname` VARCHAR(50) DEFAULT '',
  `role` VARCHAR(20) DEFAULT 'admin',
  `token` VARCHAR(64) DEFAULT NULL,
  `is_active` TINYINT(1) DEFAULT 1,
  `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 分类表
CREATE TABLE IF NOT EXISTS `categories` (
  `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  `name` VARCHAR(50) NOT NULL UNIQUE,
  `color` VARCHAR(20) DEFAULT '#4C6FFF',
  `sort_order` INT DEFAULT 0,
  `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 软件表
CREATE TABLE IF NOT EXISTS `apps` (
  `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  `category_id` INT UNSIGNED DEFAULT NULL,
  `name` VARCHAR(100) NOT NULL,
  `icon` VARCHAR(255) DEFAULT '',
  `version` VARCHAR(50) DEFAULT '',
  `description` TEXT,
  `package_name` VARCHAR(100) DEFAULT '',
  `download_count` INT UNSIGNED DEFAULT 0,
  `rating` DECIMAL(2,1) DEFAULT 0.0,
  `sort_order` INT DEFAULT 0,
  `is_active` TINYINT(1) DEFAULT 1,
  `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  `updated_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  KEY `idx_category` (`category_id`),
  KEY `idx_active` (`is_active`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 网盘推广链接表
CREATE TABLE IF NOT EXISTS `pan_links` (
  `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  `app_id` INT UNSIGNED NOT NULL,
  `pan_type` VARCHAR(20) DEFAULT 'uc',
  `label` VARCHAR(50) DEFAULT '',
  `url` TEXT NOT NULL,
  `password` VARCHAR(50) DEFAULT '',
  `sort_order` INT DEFAULT 0,
  `is_active` TINYINT(1) DEFAULT 1,
  `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  KEY `idx_app` (`app_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 点击统计表
CREATE TABLE IF NOT EXISTS `clicks` (
  `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  `link_id` INT UNSIGNED NOT NULL,
  `ip` VARCHAR(64) DEFAULT '',
  `user_agent` VARCHAR(255) DEFAULT '',
  `referer` VARCHAR(255) DEFAULT '',
  `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  KEY `idx_link_time` (`link_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 默认管理员 (密码 REDACTED_DB_PASS 的 bcrypt 哈希, 由服务器 PHP password_hash 生成)
INSERT IGNORE INTO `users` (`username`, `password`, `nickname`, `role`) VALUES
('zjyzjy', '$2y$12$p/cJv53Q6/AN4qePRcxy9u.zxAmYA61MC3AQqqLQApsXNS4EyLK2m', '风铃管理员', 'admin');

-- 设置表 (版本检测等配置)
CREATE TABLE IF NOT EXISTS `settings` (
    `id` INT AUTO_INCREMENT PRIMARY KEY,
    `key` VARCHAR(64) NOT NULL UNIQUE,
    `value` TEXT,
    `updated_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 轮播图
CREATE TABLE IF NOT EXISTS `banners` (
    `id` INT AUTO_INCREMENT PRIMARY KEY,
    `image` VARCHAR(500) NOT NULL,
    `title` VARCHAR(100) DEFAULT '',
    `app_id` INT DEFAULT NULL,
    `url` VARCHAR(500) DEFAULT '',
    `sort_order` INT DEFAULT 0,
    `is_active` TINYINT DEFAULT 1,
    `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- 兼容已有表 (MySQL 5.7 无 ADD COLUMN IF NOT EXISTS)
SET @col_exists = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='flfxk' AND TABLE_NAME='banners' AND COLUMN_NAME='url');
SET @sql = IF(@col_exists = 0, 'ALTER TABLE `banners` ADD COLUMN `url` VARCHAR(500) DEFAULT \'\' AFTER `app_id`', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- apps 表加整合包字段 (pack_id 指向父整合包, NULL=独立软件)
-- 注意: MySQL 5.7 不支持 ADD COLUMN IF NOT EXISTS, 若已执行过则忽略此行
SET @col_exists = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='flfxk' AND TABLE_NAME='apps' AND COLUMN_NAME='pack_id');
SET @sql = IF(@col_exists = 0, 'ALTER TABLE `apps` ADD COLUMN `pack_id` INT DEFAULT NULL AFTER `sort_order`', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 置顶/精选标记
SET @col_exists = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='flfxk' AND TABLE_NAME='apps' AND COLUMN_NAME='is_top');
SET @sql = IF(@col_exists = 0, 'ALTER TABLE `apps` ADD COLUMN `is_top` TINYINT DEFAULT 0 AFTER `pack_id`', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @col_exists = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='flfxk' AND TABLE_NAME='apps' AND COLUMN_NAME='is_featured');
SET @sql = IF(@col_exists = 0, 'ALTER TABLE `apps` ADD COLUMN `is_featured` TINYINT DEFAULT 0 AFTER `is_top`', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 介绍图片 (JSON 数组) + 投稿人 QQ
SET @col_exists = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='flfxk' AND TABLE_NAME='apps' AND COLUMN_NAME='screenshots');
SET @sql = IF(@col_exists = 0, 'ALTER TABLE `apps` ADD COLUMN `screenshots` TEXT DEFAULT NULL AFTER `description`', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @col_exists = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='flfxk' AND TABLE_NAME='apps' AND COLUMN_NAME='contributor_qq');
SET @sql = IF(@col_exists = 0, 'ALTER TABLE `apps` ADD COLUMN `contributor_qq` VARCHAR(20) DEFAULT '''' AFTER `screenshots`', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 默认版本配置 (后续通过管理端更新)
INSERT IGNORE INTO `settings` (`key`, `value`) VALUES
('latest_version', '{"version":"1.0.0","url":"","update_log":""}');


-- 崩溃日志上报 (App 端崩溃自动上传, 管理端「设置→崩溃」tab 查看)
CREATE TABLE IF NOT EXISTS crash_reports (
  id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  device VARCHAR(100) DEFAULT '',
  android_version VARCHAR(30) DEFAULT '',
  app_version VARCHAR(30) DEFAULT '',
  stack TEXT,
  ip VARCHAR(45) DEFAULT '',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
