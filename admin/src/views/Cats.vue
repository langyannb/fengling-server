<template>
  <a-card :bordered="false" class="page-card">
    <div class="page-toolbar">
      <a-space>
        <a-button type="primary" @click="openCat()">
          <template #icon><icon-plus /></template>添加分类
        </a-button>
        <a-button @click="load">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </a-space>
      <span class="muted">共 {{ list.length }} 个分类 (顶级 {{ topCats.length }} 个)</span>
    </div>

    <!-- ============ 手机端: 卡片列表 (替代必须横滑的表格) ============ -->
    <template v-if="isMobile">
      <a-empty v-if="!sortedCats.length" description="暂无分类" />
      <div v-else class="m-cards">
        <div v-for="record in sortedCats" :key="record.id" class="m-card">
          <div class="m-card-head">
            <img v-if="record.icon" class="m-card-thumb" :src="record.icon" />
            <a-avatar v-else :size="44" :style="{ backgroundColor: record.color || '#4C6FFF' }">
              {{ (record.name || '?')[0] }}
            </a-avatar>
            <div class="m-card-title">
              <span v-if="record.parent_id" class="muted">子分类 · </span>{{ record.name }}
            </div>
          </div>

          <div class="m-card-row">
            <span class="m-card-label">父分类</span>
            <span class="m-card-value">
              <a-tag v-if="record.parent_id" color="arcoblue">{{ catName(record.parent_id) || '未知' }}</a-tag>
              <a-tag v-else color="gray">顶级分类</a-tag>
            </span>
          </div>

          <div class="m-card-row">
            <span class="m-card-label">颜色</span>
            <span class="m-card-value">
              <span class="dot" :style="{ background: record.color || '#4C6FFF' }"></span>
              <span class="muted">{{ record.color || '默认' }}</span>
            </span>
          </div>

          <div class="m-card-row">
            <span class="m-card-label">排序</span>
            <span class="m-card-value">{{ record.sort_order }}</span>
          </div>

          <div class="m-card-actions">
            <a-button type="text" size="small" @click="openCat(record)">编辑</a-button>
            <a-popconfirm content="确认删除该分类?" @ok="delCat(record)">
              <a-button type="text" size="small" status="danger">删除</a-button>
            </a-popconfirm>
          </div>
        </div>
      </div>
    </template>

    <!-- ============ 桌面端: 原表格, 渲染路径保持不变 ============ -->
    <!-- 横向总宽: 70+180+140+140+90+160 = 780, 取 820 留余量, 手机端由 tableScroll 兜底 >=720 -->
    <a-table v-else :data="sortedCats" :loading="loading" row-key="id" size="small" :scroll="scrollX">
      <template #columns>
        <a-table-column title="图标" :width="70">
          <template #cell="{ record }">
            <img v-if="record.icon" class="thumb" :src="record.icon" />
            <a-avatar v-else :size="40" :style="{ backgroundColor: record.color || '#4C6FFF' }">
              {{ (record.name || '?')[0] }}
            </a-avatar>
          </template>
        </a-table-column>

        <a-table-column title="名称" :width="180">
          <template #cell="{ record }">
            <span v-if="record.parent_id" class="muted">子分类 · </span>{{ record.name }}
          </template>
        </a-table-column>

        <a-table-column title="父分类" :width="140">
          <template #cell="{ record }">
            <a-tag v-if="record.parent_id" color="arcoblue">{{ catName(record.parent_id) || '未知' }}</a-tag>
            <a-tag v-else color="gray">顶级分类</a-tag>
          </template>
        </a-table-column>

        <a-table-column title="颜色" :width="140">
          <template #cell="{ record }">
            <span class="dot" :style="{ background: record.color || '#4C6FFF' }"></span>
            <span class="muted">{{ record.color || '默认' }}</span>
          </template>
        </a-table-column>

        <a-table-column title="排序" data-index="sort_order" :width="90" />

        <a-table-column title="操作" :width="160">
          <template #cell="{ record }">
            <a-space>
              <a-button type="text" size="small" @click="openCat(record)">编辑</a-button>
              <a-popconfirm content="确认删除该分类?" @ok="delCat(record)">
                <a-button type="text" size="small" status="danger">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无分类</template>
    </a-table>

    <a-modal
      v-model:visible="showCat"
      :title="form.id ? '编辑分类' : '添加分类'"
      :width="modalWidth(600)"
      :ok-loading="saving"
      unmount-on-close
      @ok="saveCat"
    >
      <a-form :model="form" layout="vertical">
        <a-form-item field="name" label="名称 *">
          <a-input v-model="form.name" placeholder="分类名" allow-clear />
        </a-form-item>

        <a-form-item field="parent_id" label="父分类 (留空为顶级分类)">
          <a-select v-model="form.parent_id" placeholder="— 顶级分类 —" allow-clear>
            <a-option :value="0">— 顶级分类 —</a-option>
            <a-option v-for="p in topCats" :key="p.id" :value="p.id" :disabled="p.id === form.id">
              {{ p.name }}
            </a-option>
          </a-select>
        </a-form-item>

        <a-form-item field="icon" label="图标 (点击上传)">
          <a-upload
            :show-file-list="false"
            accept="image/*"
            :custom-request="customUpload"
            :disabled="uploading"
          >
            <template #upload-button>
              <div class="up-box" :class="{ 'up-box--disabled': uploading }">
                <img v-if="form.icon" :src="form.icon" class="up-img" />
                <template v-else>
                  <icon-plus :size="20" />
                  <span>上传分类图标</span>
                </template>
                <div v-if="uploading" class="up-tip">上传中...</div>
              </div>
            </template>
          </a-upload>
          <a-button v-if="form.icon" type="text" size="small" status="danger" @click="form.icon = ''">
            清除图标
          </a-button>
          <a-progress v-if="uploading" :percent="progress" size="small" style="margin-top: 6px" />
        </a-form-item>

        <a-form-item field="color" label="颜色">
          <div class="color-row">
            <a-color-picker v-model="form.color" size="small" />
            <div
              v-for="c in COLORS"
              :key="c"
              class="color-swatch"
              :class="{ 'color-swatch--on': form.color === c }"
              :style="{ background: c }"
              @click="form.color = c"
            ></div>
          </div>
        </a-form-item>

        <a-form-item field="sort_order" label="排序">
          <a-input-number v-model="form.sort_order" :min="0" style="width: 100%" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, uploadFile, pickList } from '../api'
import { isMobile, modalWidth, tableScroll } from '../composables/useResponsive'

// 表格横向滚动: 桌面按列宽总和, 手机端至少 720
const scrollX = tableScroll(820)

// 与原后台 catForm 默认色一致
const COLORS = ['#4C6FFF', '#22B07D', '#FF8F1F', '#FF4D6D', '#7C4DFF', '#00B8D4', '#FFB300', '#F5455C']

const list = ref([])
const loading = ref(false)
const showCat = ref(false)
const saving = ref(false)
const uploading = ref(false)
const progress = ref(0)

const form = reactive({
  id: 0,
  name: '',
  parent_id: 0,
  color: '#4C6FFF',
  icon: '',
  sort_order: 0,
})

// 顶级分类 (父分类下拉只列顶级分类)
const topCats = computed(() => list.value.filter((c) => !c.parent_id))

// 排序: 顶级在前, 子分类跟随父分类 (与原 sortedCats 一致)
const sortedCats = computed(() =>
  [...list.value].sort(
    (a, b) =>
      (a.parent_id || 0) - (b.parent_id || 0) ||
      (a.sort_order || 0) - (b.sort_order || 0) ||
      a.id - b.id
  )
)

/** 按 id 查分类名 (列表显示父分类名) */
function catName(id) {
  const c = list.value.find((x) => x.id === id)
  return c ? c.name : ''
}

async function load() {
  loading.value = true
  try {
    const r = await api('categories')
    list.value = pickList(r)
  } finally {
    loading.value = false
  }
}

function openCat(record) {
  if (record) {
    Object.assign(form, {
      id: record.id,
      name: record.name || '',
      parent_id: Number(record.parent_id) || 0,
      color: record.color || '#4C6FFF',
      icon: record.icon || '',
      sort_order: Number(record.sort_order) || 0,
    })
  } else {
    Object.assign(form, {
      id: 0,
      name: '',
      parent_id: 0,
      color: '#4C6FFF',
      icon: '',
      sort_order: 0,
    })
  }
  progress.value = 0
  showCat.value = true
}

/** a-upload 自定义上传: 走 uploadFile('upload'), 成功取 res.data.url */
async function customUpload(option) {
  const file = option.fileItem && option.fileItem.file
  if (!file) return
  uploading.value = true
  progress.value = 0
  const up = await uploadFile('upload', file, (p) => {
    progress.value = p
  })
  uploading.value = false
  if (up.code === 0 && up.data && up.data.url) {
    form.icon = up.data.url
    progress.value = 100
    option.onSuccess(up.data)
    Message.success('上传成功')
  } else {
    progress.value = 0
    option.onError(up)
    if (up.code !== 401) Message.error('上传失败: ' + (up.msg || '未知错误'))
  }
}

async function saveCat() {
  if (!form.name) {
    Message.warning('请输入分类名')
    return
  }
  saving.value = true
  try {
    // 字段名与原后台保持一致: name / parent_id / color / icon / sort_order
    const payload = {
      name: form.name,
      parent_id: Number(form.parent_id) || 0,
      color: form.color || '',
      icon: form.icon || '',
      sort_order: Number(form.sort_order) || 0,
    }
    const r = form.id
      ? await api('category_update', { id: form.id, ...payload }, 'POST')
      : await api('category_create', payload, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    showCat.value = false
    Message.success('保存成功')
    load()
  } finally {
    saving.value = false
  }
}

async function delCat(record) {
  const r = await api('category_delete', { id: record.id }, 'POST')
  if (r.code !== 0) {
    if (r.code !== 401) Message.error('删除失败: ' + (r.msg || '未知错误'))
    return
  }
  Message.success('删除成功')
  load()
}

onMounted(load)
</script>

<style scoped>
.page-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.muted {
  color: var(--color-text-3);
  font-size: 13px;
}
/* 缩略图统一走全局 .thumb, 这里不再覆写尺寸 */
.dot {
  display: inline-block;
  width: 16px;
  height: 16px;
  border-radius: 4px;
  margin-right: 6px;
  vertical-align: -3px;
  border: 1px solid var(--color-border-2);
}
.up-box {
  position: relative;
  width: 120px;
  height: 120px;
  border: 1px dashed var(--color-border-3);
  border-radius: 6px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  color: var(--color-text-3);
  overflow: hidden;
  cursor: pointer;
}
.up-box--disabled {
  cursor: not-allowed;
  opacity: 0.7;
}
.up-img {
  width: 100%;
  height: 100%;
  object-fit: contain;
}
.up-tip {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-mask-bg, rgba(0, 0, 0, 0.45));
  color: var(--color-white, #fff);
  font-size: 13px;
}
/* 颜色块与列表内的颜色圆点统一圆角与描边 */
.color-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.color-swatch {
  width: 28px;
  height: 28px;
  border-radius: 4px;
  border: 1px solid var(--color-border-2);
  cursor: pointer;
  outline-offset: 2px;
}
.color-swatch--on {
  outline: 2px solid var(--color-text-2);
}

/* 手机端卡片: 图标缩略图用 contain, 避免非正方形图标被裁 */
.m-card .m-card-thumb {
  object-fit: contain;
  background: var(--color-fill-2);
}

/* ============ 手机 (<=820px) ============ */
@media (max-width: 820px) {
  .page-toolbar {
    flex-direction: column;
    align-items: stretch;
  }
  /* 上传控件占满一行, 触摸更好点 */
  .up-box {
    width: 100%;
    max-width: 100%;
  }
  .color-swatch {
    width: 32px;
    height: 32px;
  }
}
</style>
