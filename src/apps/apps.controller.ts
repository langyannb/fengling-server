// 风铃分享库 - 软件控制器
// GET /apps 公开; 管理操作需 JWT
import {
  Body,
  Controller,
  Delete,
  Get,
  Headers,
  Ip,
  Param,
  ParseIntPipe,
  Post,
  Put,
  Query,
  UseGuards,
} from '@nestjs/common';
import { ApiBearerAuth, ApiOperation, ApiTags } from '@nestjs/swagger';
import { JwtAuthGuard } from '../auth/jwt-auth.guard';
import { AppsService } from './apps.service';

@ApiTags('软件')
@Controller('apps')
export class AppsController {
  constructor(private readonly appsService: AppsService) {}

  @Get()
  @ApiOperation({ summary: '软件列表 (公开)' })
  findAll(@Query('categoryId') categoryId?: string, @Query('keyword') keyword?: string) {
    return this.appsService.findAll({
      categoryId: categoryId ? Number(categoryId) : undefined,
      keyword,
    });
  }

  @Get('stats')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth()
  @ApiOperation({ summary: '推广统计 (需登录)' })
  stats() {
    return this.appsService.stats();
  }

  @Get(':id')
  @ApiOperation({ summary: '软件详情 (公开)' })
  findOne(@Param('id', ParseIntPipe) id: number) {
    return this.appsService.findOne(id);
  }

  @Post()
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth()
  @ApiOperation({ summary: '创建软件 (需登录)' })
  create(@Body() body: Record<string, unknown>) {
    return this.appsService.create(body as never);
  }

  @Put(':id')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth()
  @ApiOperation({ summary: '更新软件 (需登录)' })
  update(
    @Param('id', ParseIntPipe) id: number,
    @Body() body: Record<string, unknown>,
  ) {
    return this.appsService.update(id, body as never);
  }

  @Delete(':id')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth()
  @ApiOperation({ summary: '删除软件 (需登录)' })
  remove(@Param('id', ParseIntPipe) id: number) {
    return this.appsService.remove(id);
  }

  @Post(':id/pan-links')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth()
  @ApiOperation({ summary: '添加网盘推广链接 (需登录)' })
  addPanLink(
    @Param('id', ParseIntPipe) id: number,
    @Body() body: Record<string, unknown>,
  ) {
    return this.appsService.addPanLink(id, body as never);
  }

  @Delete('pan-links/:linkId')
  @UseGuards(JwtAuthGuard)
  @ApiBearerAuth()
  @ApiOperation({ summary: '删除网盘链接 (需登录)' })
  removePanLink(@Param('linkId', ParseIntPipe) linkId: number) {
    return this.appsService.removePanLink(linkId);
  }

  @Post('click/:linkId')
  @ApiOperation({ summary: '点击下载 (公开, 记录统计)' })
  clickLink(
    @Param('linkId', ParseIntPipe) linkId: number,
    @Ip() ip: string,
    @Headers('user-agent') userAgent?: string,
    @Headers('referer') referer?: string,
  ) {
    return this.appsService.clickLink(linkId, {
      ip,
      userAgent,
      referer,
    });
  }
}
