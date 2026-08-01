import 'dotenv/config';
import { NestFactory } from '@nestjs/core';
import { ValidationPipe } from '@nestjs/common';
import { DocumentBuilder, SwaggerModule } from '@nestjs/swagger';
import { AppModule } from './app.module';
import { config } from './config';

async function bootstrap() {
  const app = await NestFactory.create(AppModule);

  // 允许移动端跨域访问
  app.enableCors({
    origin: true,
    credentials: true,
  });

  app.setGlobalPrefix('api');

  // 参数校验
  app.useGlobalPipes(
    new ValidationPipe({
      whitelist: true,
      transform: true,
    }),
  );

  // Swagger 文档
  const swaggerConfig = new DocumentBuilder()
    .setTitle('风铃分享库 API')
    .setDescription('软件库 + 网盘推广管理系统')
    .setVersion('1.0')
    .addBearerAuth()
    .build();
  const document = SwaggerModule.createDocument(app, swaggerConfig);
  SwaggerModule.setup('api/docs', app, document);

  await app.listen(config.port);
  console.log(`[bootstrap] 风铃分享库 API 已启动: http://0.0.0.0:${config.port}`);
  console.log(`[bootstrap] Swagger 文档: http://0.0.0.0:${config.port}/api/docs`);
}

void bootstrap();
