// 风铃分享库 - 分类服务
import { Injectable, NotFoundException } from '@nestjs/common';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import { Category } from '../entities/category.entity';

@Injectable()
export class CategoryService {
  constructor(
    @InjectRepository(Category)
    private readonly categoryRepository: Repository<Category>,
  ) {}

  findAll(): Promise<Category[]> {
    return this.categoryRepository.find({
      order: { sortOrder: 'ASC', id: 'ASC' },
    });
  }

  findOne(id: number): Promise<Category | null> {
    return this.categoryRepository.findOneBy({ id });
  }

  async create(data: Partial<Category>): Promise<Category> {
    const category = this.categoryRepository.create(data);
    return this.categoryRepository.save(category);
  }

  async update(id: number, data: Partial<Category>): Promise<Category> {
    const category = await this.findOne(id);
    if (!category) throw new NotFoundException('分类不存在');
    Object.assign(category, data);
    return this.categoryRepository.save(category);
  }

  async remove(id: number): Promise<void> {
    const category = await this.findOne(id);
    if (!category) throw new NotFoundException('分类不存在');
    await this.categoryRepository.remove(category);
  }
}
