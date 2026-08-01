// 风铃分享库 - 软件分类实体
import {
  Column,
  CreateDateColumn,
  Entity,
  OneToMany,
  PrimaryGeneratedColumn,
} from 'typeorm';
import { AppItem } from './app.entity';

@Entity('categories')
export class Category {
  @PrimaryGeneratedColumn()
  id: number;

  @Column({ unique: true, length: 50 })
  name: string;

  @Column({ length: 20, default: '#4C6FFF' })
  color: string;

  @Column({ default: 0 })
  sortOrder: number;

  @OneToMany(() => AppItem, (app) => app.category)
  apps: AppItem[];

  @CreateDateColumn()
  createdAt: Date;
}
