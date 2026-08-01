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
