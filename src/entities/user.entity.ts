// 风铃分享库 - 用户实体
// 默认管理员账号: zjyzjy / REDACTED_DB_PASS (bcrypt 哈希后入库)
import {
  Column,
  CreateDateColumn,
  Entity,
  PrimaryGeneratedColumn,
  UpdateDateColumn,
} from 'typeorm';

@Entity('users')
export class User {
  @PrimaryGeneratedColumn()
  id: number;

  @Column({ unique: true, length: 50 })
  username: string;

  @Column({ length: 100, select: false })
  password: string;

  @Column({ length: 50, default: 'admin' })
  role: string;

  @Column({ length: 100, nullable: true })
  nickname: string;

  @Column({ default: true })
  isActive: boolean;

  @CreateDateColumn()
  createdAt: Date;

  @UpdateDateColumn()
  updatedAt: Date;
}
