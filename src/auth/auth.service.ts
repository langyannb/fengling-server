// 风铃分享库 - 认证服务
// 默认管理员: zjyzjy / REDACTED_DB_PASS
// 首次启动自动创建默认账号 (bcrypt 哈希)
import { Injectable, UnauthorizedException } from '@nestjs/common';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import * as bcrypt from 'bcryptjs';
import { JwtService } from '@nestjs/jwt';
import { User } from '../entities/user.entity';

export const DEFAULT_ADMIN = {
  username: 'zjyzjy',
  password: 'REDACTED_DB_PASS',
};

@Injectable()
export class AuthService {
  constructor(
    @InjectRepository(User)
    private readonly userRepository: Repository<User>,
    private readonly jwtService: JwtService,
  ) {}

  /** 首次启动: 若用户表为空则创建默认管理员 */
  async ensureDefaultAdmin(): Promise<void> {
    const count = await this.userRepository.count();
    if (count > 0) return;
    const hashed = await bcrypt.hash(DEFAULT_ADMIN.password, 10);
    await this.userRepository.save(
      this.userRepository.create({
        username: DEFAULT_ADMIN.username,
        password: hashed,
        role: 'admin',
        nickname: '风铃管理员',
      }),
    );
    console.log('[auth] 已创建默认管理员账号:', DEFAULT_ADMIN.username);
  }

  /** 登录校验 */
  async validateUser(username: string, password: string): Promise<User | null> {
    const user = await this.userRepository
      .createQueryBuilder('user')
      .addSelect('user.password')
      .where('user.username = :username', { username })
      .getOne();
    if (!user || !user.isActive) return null;
    const ok = await bcrypt.compare(password, user.password);
    return ok ? user : null;
  }

  /** 签发 JWT */
  async login(username: string, password: string) {
    const user = await this.validateUser(username, password);
    if (!user) {
      throw new UnauthorizedException('用户名或密码错误');
    }
    const payload = { sub: user.id, username: user.username, role: user.role };
    return {
      accessToken: await this.jwtService.signAsync(payload),
      user: {
        id: user.id,
        username: user.username,
        role: user.role,
        nickname: user.nickname,
      },
    };
  }
}
