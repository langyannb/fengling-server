// 风铃分享库 - 软件服务
// 软件 CRUD + 网盘推广链接 + 点击统计
import {
  Injectable,
  NotFoundException,
} from '@nestjs/common';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import { AppItem } from '../entities/app.entity';
import { PanLink } from '../entities/pan-link.entity';
import { Click } from '../entities/click.entity';

@Injectable()
export class AppsService {
  constructor(
    @InjectRepository(AppItem)
    private readonly appRepository: Repository<AppItem>,
    @InjectRepository(PanLink)
    private readonly panLinkRepository: Repository<PanLink>,
    @InjectRepository(Click)
    private readonly clickRepository: Repository<Click>,
  ) {}

  /** 软件列表 (公开, 仅上架) */
  findAll(query: { categoryId?: number; keyword?: string } = {}): Promise<AppItem[]> {
    const qb = this.appRepository
      .createQueryBuilder('app')
      .leftJoinAndSelect('app.category', 'category')
      .leftJoinAndSelect('app.panLinks', 'panLinks')
      .where('app.isActive = :active', { active: true })
      .orderBy('app.sortOrder', 'ASC')
      .addOrderBy('app.id', 'DESC');

    if (query.categoryId) {
      qb.andWhere('app.categoryId = :categoryId', {
        categoryId: query.categoryId,
      });
    }
    if (query.keyword) {
      qb.andWhere('app.name LIKE :keyword', {
        keyword: `%${query.keyword}%`,
      });
    }
    return qb.getMany();
  }

  /** 软件详情 (公开) */
  async findOne(id: number): Promise<AppItem> {
    const app = await this.appRepository.findOne({
      where: { id },
      relations: { category: true, panLinks: true },
    });
    if (!app || !app.isActive) throw new NotFoundException('软件不存在');
    return app;
  }

  /** 创建软件 (需登录) */
  create(data: Partial<AppItem>): Promise<AppItem> {
    const app = this.appRepository.create(data);
    return this.appRepository.save(app);
  }

  /** 更新软件 (需登录) */
  async update(id: number, data: Partial<AppItem>): Promise<AppItem> {
    const app = await this.appRepository.findOneBy({ id });
    if (!app) throw new NotFoundException('软件不存在');
    Object.assign(app, data);
    return this.appRepository.save(app);
  }

  /** 删除软件 (需登录) */
  async remove(id: number): Promise<void> {
    const app = await this.appRepository.findOneBy({ id });
    if (!app) throw new NotFoundException('软件不存在');
    await this.appRepository.remove(app);
  }

  /** 为软件添加网盘链接 (需登录) */
  async addPanLink(
    appId: number,
    data: Partial<PanLink>,
  ): Promise<PanLink> {
    const app = await this.appRepository.findOneBy({ id: appId });
    if (!app) throw new NotFoundException('软件不存在');
    const link = this.panLinkRepository.create({ ...data, app });
    return this.panLinkRepository.save(link);
  }

  /** 删除网盘链接 (需登录) */
  async removePanLink(linkId: number): Promise<void> {
    const link = await this.panLinkRepository.findOneBy({ id: linkId });
    if (!link) throw new NotFoundException('链接不存在');
    await this.panLinkRepository.remove(link);
  }

  /** 点击下载 (公开): 记录统计 + 返回网盘链接 */
  async clickLink(linkId: number, ctx: {
    ip?: string;
    userAgent?: string;
    referer?: string;
  }): Promise<{ url: string; password?: string }> {
    const link = await this.panLinkRepository.findOne({
      where: { id: linkId, isActive: true },
    });
    if (!link) throw new NotFoundException('链接不存在');

    // 记录点击
    const click = this.clickRepository.create({
      link,
      ip: ctx.ip?.slice(0, 64),
      userAgent: ctx.userAgent?.slice(0, 200),
      referer: ctx.referer?.slice(0, 20),
    });
    await this.clickRepository.save(click);

    // 下载量 +1
    if (link.app) {
      await this.appRepository.increment(
        { id: (link.app as AppItem).id },
        'downloadCount',
        1,
      );
    }

    return { url: link.url, password: link.password };
  }

  /** 统计 (需登录): 总下载量 + 今日点击 + 各链接点击 */
  async stats() {
    const totalApps = await this.appRepository.count();
    const totalDownloads = await this.appRepository
      .createQueryBuilder('app')
      .select('COALESCE(SUM(app.downloadCount), 0)', 'total')
      .getRawOne();
    const todayStart = new Date();
    todayStart.setHours(0, 0, 0, 0);
    const todayClicks = await this.clickRepository
      .createQueryBuilder('click')
      .where('click.createdAt >= :today', { today: todayStart })
      .getCount();
    const totalClicks = await this.clickRepository.count();

    const topLinks = await this.clickRepository
      .createQueryBuilder('click')
      .leftJoinAndSelect('click.link', 'link')
      .select('link.id', 'linkId')
      .addSelect('link.label', 'label')
      .addSelect('COUNT(click.id)', 'count')
      .groupBy('link.id')
      .orderBy('COUNT(click.id)', 'DESC')
      .limit(10)
      .getRawMany();

    return {
      totalApps,
      totalDownloads: Number(totalDownloads?.total || 0),
      todayClicks,
      totalClicks,
      topLinks,
    };
  }
}
