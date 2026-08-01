// 风铃分享库 - 推广点击统计实体
// 每次用户点击网盘下载链接记一条, 用于统计推广收益
import {
  Column,
  CreateDateColumn,
  Entity,
  Index,
  JoinColumn,
  ManyToOne,
  PrimaryGeneratedColumn,
} from 'typeorm';
import { PanLink } from './pan-link.entity';

@Entity('clicks')
@Index(['linkId', 'createdAt'])
export class Click {
  @PrimaryGeneratedColumn()
  id: number;

  @ManyToOne(() => PanLink, (link) => link.clicks, { onDelete: 'CASCADE' })
  @JoinColumn({ name: 'linkId' })
  link: PanLink;

  @Column({ length: 64, nullable: true })
  ip: string;

  @Column({ length: 200, nullable: true })
  userAgent: string;

  @Column({ length: 20, nullable: true })
  referer: string;

  @CreateDateColumn()
  createdAt: Date;
}
