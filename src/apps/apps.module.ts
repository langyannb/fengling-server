// 风铃分享库 - 软件模块
import { Module } from '@nestjs/common';
import { TypeOrmModule } from '@nestjs/typeorm';
import { AppItem } from '../entities/app.entity';
import { PanLink } from '../entities/pan-link.entity';
import { Click } from '../entities/click.entity';
import { AppsController } from './apps.controller';
import { AppsService } from './apps.service';

@Module({
  imports: [TypeOrmModule.forFeature([AppItem, PanLink, Click])],
  controllers: [AppsController],
  providers: [AppsService],
  exports: [AppsService],
})
export class AppsModule {}
