// 风铃分享库 - 网盘推广链接实体
// 每个软件可挂多个网盘源 (UC/夸克/百度...), 链接带推广参数
import {
  Column,
  CreateDateColumn,
  Entity,
  JoinColumn,
  ManyToOne,
  OneToMany,
  PrimaryGeneratedColumn,
} from 'typeorm';
import { AppItem } from './app.entity';
import { Click } from './click.entity';

export enum PanType {
  UC = 'uc', // UC网盘
  QUARK = 'quark', // 夸克网盘
  BAIDU = 'baidu', // 百度网盘
  ALI = 'ali', // 阿里云盘
  OTHER = 'other',
}

@Entity('pan_links')
export class PanLink {
  @PrimaryGeneratedColumn()
  id: number;

  @ManyToOne(() => AppItem, (app) => app.panLinks, { onDelete: 'CASCADE' })
  @JoinColumn({ name: 'appId' })
  app: AppItem;

  @Column({ length: 20, default: PanType.UC })
  panType: string;

  @Column({ length: 50 })
  label: string;

  @Column({ type: 'text' })
  url: string;

  @Column({ length: 100, nullable: true })
  password: string;

  @Column({ default: 0 })
  sortOrder: number;

  @Column({ default: true })
  isActive: boolean;

  @OneToMany(() => Click, (click) => click.link)
  clicks: Click[];

  @CreateDateColumn()
  createdAt: Date;
}
