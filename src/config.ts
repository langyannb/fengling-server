// 风铃分享库 - 配置
// 读取 .env (本地/服务器通用), 生产环境可用环境变量覆盖
export interface AppConfig {
  port: number;
  db: {
    host: string;
    port: number;
    user: string;
    password: string;
    database: string;
    synchronize: boolean;
  };
  jwtSecret: string;
  jwtExpiresIn: string;
}

function bool(value: string | undefined, fallback: boolean): boolean {
  if (value === undefined) return fallback;
  return value === 'true' || value === '1';
}

export const config: AppConfig = {
  port: parseInt(process.env.PORT || '9845', 10),
  db: {
    host: process.env.DB_HOST || '127.0.0.1',
    port: parseInt(process.env.DB_PORT || '3306', 10),
    user: process.env.DB_USER || 'root',
    password: process.env.DB_PASSWORD || '',
    database: process.env.DB_NAME || 'fengling_share',
    synchronize: bool(process.env.DB_SYNCHRONIZE, true),
  },
  jwtSecret: process.env.JWT_SECRET || 'fengling-share-secret-2026',
  jwtExpiresIn: process.env.JWT_EXPIRES_IN || '7d',
};
