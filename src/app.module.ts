import { Module, OnModuleInit } from '@nestjs/common';
import { TypeOrmModule } from '@nestjs/typeorm';
import { AppController } from './app.controller';
import { AppService } from './app.service';
import { AuthModule } from './auth/auth.module';
import { AuthService } from './auth/auth.service';
import { CategoryModule } from './categories/category.module';
import { AppsModule } from './apps/apps.module';
import { User } from './entities/user.entity';
import { Category } from './entities/category.entity';
import { AppItem } from './entities/app.entity';
import { PanLink } from './entities/pan-link.entity';
import { Click } from './entities/click.entity';
import { config } from './config';

@Module({
  imports: [
    TypeOrmModule.forRoot({
      type: 'mysql',
      host: config.db.host,
      port: config.db.port,
      username: config.db.user,
      password: config.db.password,
      database: config.db.database,
      entities: [User, Category, AppItem, PanLink, Click],
      synchronize: config.db.synchronize,
      charset: 'utf8mb4',
    }),
    AuthModule,
    CategoryModule,
    AppsModule,
  ],
  controllers: [AppController],
  providers: [AppService],
})
export class AppModule implements OnModuleInit {
  constructor(private readonly authService: AuthService) {}

  /** 启动时确保默认管理员账号存在 */
  async onModuleInit(): Promise<void> {
    await this.authService.ensureDefaultAdmin();
  }
}
