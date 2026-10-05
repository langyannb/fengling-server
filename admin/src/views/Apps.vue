<template>
  <div>
    <a-card :bordered="false" class="page-card">
      <!-- 筛选工具栏：手机端自动换行、逐行铺满 -->
      <div class="page-toolbar">
        <a-input
          v-model="kw"
          class="grow"
          placeholder="搜索软件名 / 包名 / 投稿QQ"
          allow-clear
          :style="isMobile ? 'width:100%' : 'width:240px'"
        >
          <template #prefix><icon-search /></template>
        </a-input>
        <a-select
          v-model="filterCat"
          placeholder="全部分类"
          allow-clear
          :options="catFilterOptions"
          :style="isMobile ? 'width:calc(50% - 3px)' : 'width:180px'"
        />
        <a-select
          v-model="filterStatus"
          :options="statusOptions"
          :style="isMobile ? 'width:calc(50% - 3px)' : 'width:140px'"
        />
        <div class="toolbar-spacer"></div>
        <a-button type="primary" @click="openApp()">
          <template #icon><icon-plus /></template>添加软件
        </a-button>
        <a-button @click="loadAll">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </div>

      <!-- ===== 手机端: 卡片列表 (桌面端渲染路径完全不变) ===== -->
      <template v-if="isMobile">
        <a-spin :loading="loading" style="width: 100%">
          <div class="m-cards">
            <div v-for="record in pageList" :key="record.id" class="m-card">
              <div class="m-card-head">
                <img v-if="record.icon" :src="record.icon" class="m-card-thumb" />
                <div v-else class="m-card-thumb m-card-thumb-text">{{ (record.name || '?')[0] }}</div>
                <div class="m-card-title">
                  {{ record.name }}
                  <a-tag v-if="record.is_new" size="small" color="arcoblue">新</a-tag>
                  <a-tag v-if="record.is_pack" size="small" color="purple">整合</a-tag>
                  <a-tag v-if="!Number(record.is_active)" size="small" color="red">下架</a-tag>
                </div>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">分类</span>
                <span class="m-card-value">{{ record.category_name || '未分类' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">版本</span>
                <span class="m-card-value">{{ record.version || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">包名</span>
                <span class="m-card-value">{{ record.package_name || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">投稿QQ</span>
                <span class="m-card-value">{{ record.contributor_qq || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">标记</span>
                <span class="m-card-value">
                  <a-tag v-if="Number(record.is_top)" size="small" color="orange">置顶</a-tag>
                  <a-tag v-if="Number(record.is_featured)" size="small" color="gold">精选</a-tag>
                  <span v-if="!Number(record.is_top) && !Number(record.is_featured)" class="text-muted">-</span>
                </span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">下载</span>
                <span class="m-card-value">{{ record.download_count }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">发布日期</span>
                <span class="m-card-value">{{ record.release_date || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">排序</span>
                <span class="m-card-value">{{ record.sort_order }}</span>
              </div>
              <div class="m-card-actions">
                <a-button type="text" size="small" status="warning" @click="toggleTop(record)">{{ Number(record.is_top) ? "取消置顶" : "置顶" }}</a-button>
                <a-button type="text" size="small" @click="openApp(record)">编辑</a-button>
                <a-popconfirm content="确认删除该软件?" @ok="delApp(record)">
                  <a-button type="text" status="danger" size="small">删除</a-button>
                </a-popconfirm>
              </div>
            </div>
          </div>
        </a-spin>
        <a-empty v-if="!loading && !filteredList.length" description="暂无数据" />
        <a-pagination
          v-if="filteredList.length"
          v-model:current="pagination.current"
          v-model:page-size="pagination.pageSize"
          :total="filteredList.length"
          :show-total="true"
          :show-page-size="true"
          class="m-pager"
        />
      </template>
      <template v-else>
      <a-table
        :data="filteredList"
        row-key="id"
        size="small"
        :loading="loading"
        :scroll="scrollX"
        :pagination="pagination"
      >
        <template #columns>
          <a-table-column title="图标" :width="72">
            <template #cell="{ record }">
              <img v-if="record.icon" :src="record.icon" class="thumb" />
              <a-avatar v-else :size="40">{{ (record.name || '?')[0] }}</a-avatar>
            </template>
          </a-table-column>
          <a-table-column title="名称" :width="240">
            <template #cell="{ record }">
              <span class="app-name">{{ record.name }}</span>
              <a-tag v-if="record.is_new" size="small" color="arcoblue">新</a-tag>
              <a-tag v-if="record.is_pack" size="small" color="purple">整合</a-tag>
              <a-tag v-if="!Number(record.is_active)" size="small" color="red">下架</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="分类" :width="120">
            <template #cell="{ record }">{{ record.category_name || '未分类' }}</template>
          </a-table-column>
          <a-table-column title="版本" data-index="version" :width="90" />
          <a-table-column title="包名" data-index="package_name" :width="180" />
          <a-table-column title="投稿QQ" data-index="contributor_qq" :width="110" />
          <a-table-column title="标记" :width="130">
            <template #cell="{ record }">
              <a-tag v-if="Number(record.is_top)" size="small" color="orange">置顶</a-tag>
              <a-tag v-if="Number(record.is_featured)" size="small" color="gold">精选</a-tag>
              <span v-if="!Number(record.is_top) && !Number(record.is_featured)" class="text-muted">-</span>
            </template>
          </a-table-column>
          <a-table-column title="下载" data-index="download_count" :width="90" />
          <a-table-column title="发布日期" data-index="release_date" :width="120" />
          <a-table-column title="排序" data-index="sort_order" :width="80" />
          <a-table-column title="操作" :width="140" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="small" status="warning" @click="toggleTop(record)">{{ Number(record.is_top) ? "取消置顶" : "置顶" }}</a-button>
              <a-button type="text" size="small" @click="openApp(record)">编辑</a-button>
              <a-popconfirm content="确认删除该软件?" @ok="delApp(record)">
                <a-button type="text" status="danger" size="small">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </template>
        <template #empty>暂无数据</template>
      </a-table>
      </template>
    </a-card>

    <a-modal
      v-model:visible="showApp"
      :title="appForm.id ? '编辑软件' : '添加软件'"
      :width="modalWidth(720)"
      :ok-loading="saving"
      ok-text="保存软件"
      cancel-text="取消"
      unmount-on-close
      @ok="saveApp"
    >
      <a-form :model="appForm" layout="vertical">
        <!-- UC 网盘一键解析：粘贴分享链接自动填名称/版本/大小，并把链接加到网盘推广链接 -->
        <div class="uc-box">
          <div class="uc-head">
            <icon-link />
            <b>UC 网盘一键解析</b>
            <span class="uc-sub">粘贴分享链接，自动识别名称 / 版本 / 大小与文件清单</span>
          </div>
          <a-space wrap>
            <a-input v-model="ucUrl" allow-clear placeholder="https://drive.uc.cn/s/xxxx" style="width: 300px" />
            <a-input v-model="ucPwd" allow-clear placeholder="提取码" style="width: 110px" />
            <a-button type="outline" :loading="ucLoading" @click="doUcResolve">解析</a-button>
            <a-button size="mini" type="text" @click="goUc">UC 未登录？去扫码</a-button>
          </a-space>
          <div v-if="ucError" class="uc-err">{{ ucError }}</div>
          <div v-if="ucFiles.length" class="uc-result">
            <div class="uc-suggest">
              <span>
                识别结果：<b>{{ ucSuggest.name || '—' }}</b>
                · 版本 <b>{{ ucSuggest.version || '—' }}</b>
                · {{ ucSuggest.size_mb }} MB
                · 共 {{ ucFiles.length }} 个文件
              </span>
              <a-space>
                <a-button size="mini" :loading="ucImporting" @click="doUcImport('images')">导入截图到图库</a-button>
                <a-button size="mini" :loading="ucImporting" @click="doUcImport('apk')">解析安装包信息</a-button>
                <a-button size="mini" type="primary" @click="applyUc">一键填入表单</a-button>
              </a-space>
            </div>
            <a-table
              :columns="ucColumns"
              :data="ucFiles"
              size="mini"
              :pagination="false"
              :scroll="{ y: 180 }"
            />
          </div>
        </div>

        <a-divider orientation="left">基本信息</a-divider>
        <a-form-item field="name" label="名称 *" :rules="[{ required: true, message: '请输入软件名' }]">
          <a-input v-model="appForm.name" placeholder="软件名" />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="category_id" label="分类">
              <a-select v-model="appForm.category_id" :options="catOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="version" label="版本">
              <a-input v-model="appForm.version" placeholder="1.0" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item field="package_name" label="包名">
          <a-input v-model="appForm.package_name" placeholder="com.example.app" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="appForm.description" :auto-size="{ minRows: 3, maxRows: 6 }" placeholder="软件介绍" />
        </a-form-item>

        <a-divider orientation="left">图标与介绍图</a-divider>
        <a-form-item label="图标 (点击上传)">
          <div class="upload-box" @click="pickImage('icon')">
            <img v-if="appForm.icon" :src="appForm.icon" class="upload-img" />
            <template v-else>
              <span class="upload-plus">+</span>
              <span>上传图标</span>
            </template>
            <div v-if="uploading === 'icon'" class="up-tip">上传中...</div>
          </div>
        </a-form-item>
        <a-form-item label="介绍图片 (应用市场风格, 可一次多选, 点击上传)">
          <div class="shot-wrap">
            <div v-for="(shot, si) in appForm.screenshots" :key="si" class="shot-item">
              <img :src="shot" class="shot-img" />
              <button type="button" class="shot-del" @click.stop="appForm.screenshots.splice(si, 1)">×</button>
            </div>
            <div class="upload-box upload-box-sm" @click="pickImage('screenshots')">
              <template v-if="uploading === 'screenshots'">
                <span class="up-txt">{{ uploadProgress || '上传中...' }}</span>
              </template>
              <template v-else><span class="upload-plus">+</span></template>
            </div>
          </div>
        </a-form-item>

        <a-divider orientation="left">发布设置</a-divider>
        <a-row :gutter="12">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="release_date" label="发布日期 (显示用)">
              <a-date-picker
                :model-value="appForm.release_date || undefined"
                value-format="YYYY-MM-DD"
                style="width: 100%"
                @change="v => (appForm.release_date = v || '')"
              />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="sort_order" label="排序 (已改为按时间, 保留备用)">
              <a-input-number v-model="appForm.sort_order" :min="0" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item label="状态">
              <a-switch :model-value="!!Number(appForm.is_active)" @change="onActiveChange">
                <template #checked>上架</template>
                <template #unchecked>下架</template>
              </a-switch>
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item label="标记">
              <a-switch :model-value="!!Number(appForm.is_top)" @change="v => (appForm.is_top = v ? 1 : 0)">
                <template #checked>🔝 置顶</template>
                <template #unchecked>不置顶</template>
              </a-switch>
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item label="精选">
              <a-switch :model-value="!!Number(appForm.is_featured)" @change="v => (appForm.is_featured = v ? 1 : 0)">
                <template #checked>⭐ 精选</template>
                <template #unchecked>非精选</template>
              </a-switch>
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item label="新版本标 (选新=3天后回普通)">
              <a-select v-model="appForm.is_new" :options="newOptions" @change="newFlagTouched = true" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item field="contributor_qq" label="投稿人 QQ (关于页头像, 不显示号码)">
          <a-input v-model="appForm.contributor_qq" placeholder="QQ号, 可留空" />
        </a-form-item>
        <div class="form-tip">「新」标完全手动控制：选「✨ 标新」→ 3 天后自动回到普通；选「不标」→ 立即取消。改版本号不会自动标新。</div>
        <a-form-item field="pack_id" label="整合包 (选择后此软件作为整合包的子项)">
          <a-select v-model="appForm.pack_id" allow-clear placeholder="独立软件" :options="packOptions" />
        </a-form-item>

        <a-divider orientation="left">网盘推广链接</a-divider>
        <div v-for="(link, i) in appForm.links" :key="link._k || i" class="link-item">
          <div class="link-info">
            <div>
              {{ link.label || '网盘链接' }}
              <span v-if="link.password">(提取码:{{ link.password }})</span>
              <a-tag v-if="link._new" size="small" color="arcoblue">新</a-tag>
            </div>
            <div class="link-url">{{ link.url }}</div>
          </div>
          <a-button size="mini" @click="link._editing = !link._editing">{{ link._editing ? '完成' : '改' }}</a-button>
          <a-button size="mini" status="danger" @click="appForm.links.splice(i, 1)">删</a-button>
        </div>
        <a-button long style="margin-bottom: 8px" @click="addLink()">+ 添加网盘链接</a-button>
        <div v-for="link in editingLinks" :key="'e' + link._k" class="link-editor">
          <a-row :gutter="12">
            <a-col :span="isMobile ? 24 : 12">
              <a-form-item label="类型">
                <a-select v-model="link.pan_type" :options="panOptions" />
              </a-form-item>
            </a-col>
            <a-col :span="isMobile ? 24 : 12">
              <a-form-item label="名称">
                <a-input v-model="link.label" />
              </a-form-item>
            </a-col>
          </a-row>
          <a-form-item label="链接URL *">
            <a-input v-model="link.url" placeholder="https://..." />
          </a-form-item>
          <a-form-item label="提取码">
            <a-input v-model="link.password" />
          </a-form-item>
        </div>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, pickList, uploadFile } from '../api'
import { isMobile, modalWidth, tableScroll } from '../composables/useResponsive'

const loading = ref(false)
const saving = ref(false)
const apps = ref([])
const cats = ref([])
const kw = ref('')
const filterCat = ref('')
const filterStatus = ref('')

// 表格横向滚动宽度: 所有列宽合计 1372px (桌面按此宽度, 手机端至少 720px)
const scrollX = tableScroll(1372)

const showApp = ref(false)
const uploading = ref('')
const uploadProgress = ref('')
const newFlagTouched = ref(false)
const origLinkIds = ref([])

// ---- UC 网盘解析 ----
const ucUrl = ref('')
const ucPwd = ref('')
const ucLoading = ref(false)
const ucImporting = ref(false)
const ucError = ref('')
const ucFiles = ref([])
const ucSuggest = ref({ name: '', version: '', size_mb: 0, kind: '', has_apk: false, images: [], packages: [] })
const ucColumns = [
  { title: '文件', dataIndex: 'name', ellipsis: true, tooltip: true },
  { title: '类型', dataIndex: 'ext', width: 80 },
  { title: '大小', width: 100, render: ({ record }) => (record.isdir ? '目录' : record.size_mb + ' MB') },
]

const emptyForm = () => ({
  id: 0, name: '', category_id: 0, icon: '', version: '', description: '',
  screenshots: [], contributor_qq: '', package_name: '', sort_order: 0,
  is_active: 1, pack_id: null, is_top: 0, is_featured: 0, release_date: '', is_new: 0,
  links: [],
})
const appForm = ref(emptyForm())

const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '上架', value: '1' },
  { label: '下架', value: '0' },
]
const newOptions = [
  { label: '✨ 标「新」', value: 1 },
  { label: '不标', value: 0 },
]
const panOptions = [
  { label: 'UC网盘', value: 'uc' },
  { label: '夸克', value: 'quark' },
  { label: '百度', value: 'baidu' },
  { label: '阿里', value: 'ali' },
  { label: '其他', value: 'other' },
]

/** 分类排序: 顶级在前, 子分类跟随父分类 */
const sortedCats = computed(() =>
  [...cats.value].sort((a, b) =>
    (a.parent_id || 0) - (b.parent_id || 0) || (a.sort_order || 0) - (b.sort_order || 0) || a.id - b.id)
)
const catOptions = computed(() => [
  { label: '未分类', value: 0 },
  ...sortedCats.value.map(c => ({ label: c.parent_id ? '— ' + c.name : c.name, value: c.id })),
])
const catFilterOptions = computed(() => catOptions.value.filter(o => o.value !== 0))
const packOptions = computed(() =>
  apps.value.filter(a => a.id !== appForm.value.id && !a.pack_id).map(a => ({ label: a.name + ' (整合包)', value: a.id }))
)
const editingLinks = computed(() => appForm.value.links.filter(l => l._editing))
const filteredList = computed(() => {
  const k = kw.value.trim().toLowerCase()
  return apps.value.filter(a => {
    if (filterCat.value !== '' && filterCat.value !== null && Number(a.category_id || 0) !== Number(filterCat.value)) return false
    if (filterStatus.value !== '' && Number(a.is_active) !== Number(filterStatus.value)) return false
    if (!k) return true
    return [a.name, a.package_name, a.contributor_qq].some(v => String(v || '').toLowerCase().includes(k))
  })
})

// 分页: 桌面端表格与手机端卡片共用同一变量 (与 Crash.vue 一致)
const pagination = ref({ pageSize: 20, showTotal: true, showPageSize: true })

// 手机端卡片: 手动分页切片 (与桌面端共用同一 pagination)
const pageList = computed(() => {
  const size = Number(pagination.value.pageSize) || 20
  const total = filteredList.value.length
  const pages = Math.max(1, Math.ceil(total / size))
  const cur = Math.min(Math.max(1, Number(pagination.value.current) || 1), pages)
  const start = (cur - 1) * size
  return filteredList.value.slice(start, start + size)
})

async function loadApps() {
  loading.value = true
  const r = await api('apps', { include_inactive: 1 })  // 管理员可见已下架软件
  loading.value = false
  if (r.code === 0) apps.value = r.data || []
  else pickList(r)
}
async function loadCats() {
  const r = await api('categories')
  if (r.code === 0) cats.value = r.data || []
}
function loadAll() { loadApps(); loadCats() }

/** 图片上传: 直接选文件 (介绍图支持多选) */
function pickImage(target) {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = 'image/*'
  input.multiple = target === 'screenshots'
  input.onchange = async (e) => {
    const files = Array.from(e.target.files || [])
    if (!files.length) return
    const multi = target === 'screenshots' && files.length > 1
    uploading.value = target
    let uploaded = 0
    for (let fi = 0; fi < files.length; fi++) {
      const file = files[fi]
      if (multi) uploadProgress.value = `上传中 ${fi + 1}/${files.length}...`
      const r = await uploadFile('upload', file, p => { if (multi) uploadProgress.value = `上传中 ${fi + 1}/${files.length} (${p}%)` })
      if (r.code === 0) {
        if (target === 'icon') appForm.value.icon = r.data.url
        else if (target === 'screenshots') {
          if (!appForm.value.screenshots) appForm.value.screenshots = []
          appForm.value.screenshots.push(r.data.url)
        }
        uploaded++
      } else if (r.code !== 401) Message.error(r.msg || '上传失败')
      else return
    }
    if (multi) Message.success(`已上传 ${uploaded}/${files.length} 张`)
    else if (uploaded > 0) Message.success('上传成功')
    uploading.value = ''
    uploadProgress.value = ''
  }
  input.click()
}

function onActiveChange(v) { appForm.value.is_active = v ? 1 : 0 }

/** 打开弹窗: 新建 or 编辑 (编辑时异步拉详情含网盘链接) */
async function openApp(app) {
  newFlagTouched.value = false
  const f = emptyForm()
  if (app) {
    Object.assign(f, {
      id: app.id, name: app.name || '', category_id: Number(app.category_id) || 0,
      icon: app.icon || '', version: app.version || '', description: app.description || '',
      screenshots: app.screenshots ? [...app.screenshots] : [],
      contributor_qq: app.contributor_qq || '', package_name: app.package_name || '',
      sort_order: app.sort_order || 0, is_active: Number(app.is_active),
      pack_id: app.pack_id || null, is_top: Number(app.is_top) || 0,
      is_featured: Number(app.is_featured) || 0, release_date: app.release_date || '',
      is_new: Number(app.is_new) || 0,
    })
  }
  appForm.value = f
  origLinkIds.value = []
  showApp.value = true
  if (app && app.id) {
    const r = await api('app_detail', { id: app.id, include_inactive: 1 })  // 下架软件也能编辑
    if (r.code === 0 && r.data) {
      const d = r.data
      appForm.value = {
        ...appForm.value,
        name: d.name || appForm.value.name,
        icon: d.icon || appForm.value.icon,
        version: d.version || appForm.value.version,
        description: d.description || appForm.value.description,
        screenshots: (d.screenshots && d.screenshots.length) ? [...d.screenshots] : appForm.value.screenshots,
        contributor_qq: d.contributor_qq !== undefined ? (d.contributor_qq || '') : appForm.value.contributor_qq,
        package_name: d.package_name || appForm.value.package_name,
        pack_id: d.pack_id || null,
        is_top: Number(d.is_top) || 0,
        is_featured: Number(d.is_featured) || 0,
        release_date: d.release_date || '',
        is_new: Number(d.is_new) || 0,
        links: (d.pan_links || []).map(l => ({ ...l, _k: 'l' + l.id + Math.random(), _editing: false })),
      }
      // 记录原始链接 id (用于检测保存时被删除的链接)
      origLinkIds.value = (d.pan_links || []).map(l => l.id)
    } else if (r.code !== 401) {
      Message.warning('详情加载失败')
    }
  }
}

function resetUc() {
  ucError.value = ''
  ucFiles.value = []
  ucSuggest.value = { name: '', version: '', size_mb: 0, kind: '', has_apk: false, images: [], packages: [] }
}

function goUc() {
  location.hash = '#/uc'
}

async function doUcResolve() {
  if (!ucUrl.value.trim()) { Message.warning('请先粘贴 UC 分享链接'); return }
  ucLoading.value = true
  resetUc()
  const r = await api('uc_resolve', { url: ucUrl.value.trim(), pwd: ucPwd.value.trim() }, 'POST')
  ucLoading.value = false
  if (r.code !== 0) {
    ucError.value = r.msg || '解析失败'
    if (r.code === 401) Message.error('UC 账号未登录，请先到「UC 网盘」页扫码登录')
    return
  }
  ucFiles.value = (r.data && r.data.files) || []
  if (r.data && r.data.suggest) ucSuggest.value = r.data.suggest
  Message.success('解析成功，共 ' + ucFiles.value.length + ' 个文件')
}

/** 从 UC 下载并导入图片到图库 / 解析安装包信息（后端下载 APK 并解析包名版本图标） */
async function doUcImport(kind) {
  if (!ucUrl.value.trim()) { Message.warning('请先粘贴 UC 分享链接并解析'); return }
  ucImporting.value = true
  try {
    const r = await api('uc_import', { url: ucUrl.value.trim(), pwd: ucPwd.value.trim(), kind }, 'POST')
    if (r.code !== 0) {
      Message.error(r.msg || '导入失败')
      if (r.code === 401) Message.warning('UC 账号未登录，请先到「UC 网盘」页扫码登录')
      return
    }
    const d = r.data || {}
    if (kind === 'images') {
      const list = d.images || []
      if (list.length) {
        appForm.value.screenshots = list
        Message.success('已导入 ' + list.length + ' 张截图')
      } else {
        Message.warning('分享里没有找到图片')
      }
    } else {
      const a = d.apk
      if (a) {
        if (a.package) appForm.value.package_name = a.package
        if (a.version_name && !appForm.value.version) appForm.value.version = a.version_name
        if (a.icon_url) appForm.value.icon = a.icon_url
        Message.success('安装包解析完成：' + (a.package || '—') + ' / ' + (a.version_name || '—') + ' / ' + a.size_mb + ' MB')
      } else {
        Message.warning('分享里没有找到 apk 文件')
      }
    }
    if (d.errors && d.errors.length) Message.warning(d.errors.join('；'))
  } finally {
    ucImporting.value = false
  }
}

/** 把解析结果填进表单（名称 / 版本 / 网盘链接） */
function applyUc() {
  const f = appForm.value
  const g = ucSuggest.value
  if (g.name && !f.name) f.name = g.name
  if (g.version && !f.version) f.version = g.version
  const url = ucUrl.value.trim()
  if (url && !f.links.some(l => l.url === url)) {
    f.links.push({
      id: 0,
      app_id: f.id,
      pan_type: 'uc',
      label: 'UC网盘',
      url,
      password: ucPwd.value.trim(),
      _k: 'u' + Date.now(),
      _editing: false,
      _new: true,
    })
    Message.success('已把 UC 链接加入「网盘推广链接」')
  } else if (url) {
    Message.info('该链接已在列表中')
  }
  if (g.images && g.images.length && !f.description) {
    f.description = '截图：' + g.images.join('、')
  }
}

function addLink() {
  appForm.value.links.push({
    pan_type: 'uc', label: 'UC网盘', url: '', password: '',
    _new: true, _k: 'n' + Date.now() + Math.random(), _editing: true,
  })
}

async function saveApp() {
  const f = appForm.value
  if (!f.name) { Message.warning('请输入软件名'); return }
  saving.value = true
  try {
    // 已有链接先更新
    for (const link of f.links) {
      if (link.id && !link._new) {
        const r = await api('link_update', { id: link.id, pan_type: link.pan_type, label: link.label, url: link.url, password: link.password }, 'POST')
        if (r.code !== 0 && r.code !== 401) { Message.error('链接更新失败: ' + (r.msg || '')); return }
        if (r.code === 401) return
      }
    }
    // 删除被移除的链接
    if (f.id && origLinkIds.value.length) {
      const keptIds = new Set(f.links.filter(l => l.id).map(l => l.id))
      for (const oldId of origLinkIds.value) {
        if (!keptIds.has(oldId)) await api('link_delete', { id: oldId }, 'POST')
      }
    }
    const base = {
      name: f.name, category_id: f.category_id || null, icon: f.icon, version: f.version,
      description: f.description, screenshots: JSON.stringify(f.screenshots || []),
      contributor_qq: f.contributor_qq || '', package_name: f.package_name,
      sort_order: Number(f.sort_order) || 0, pack_id: f.pack_id || null,
      is_top: Number(f.is_top) || 0, is_featured: Number(f.is_featured) || 0,
      release_date: f.release_date || '',
      ...(newFlagTouched.value ? { new_flag: Number(f.is_new) ? 1 : 0 } : {}),
    }
    let r
    if (f.id) {
      r = await api('app_update', { id: f.id, ...base, is_active: Number(f.is_active) }, 'POST')
    } else {
      r = await api('app_create', base, 'POST')
      if (r.code === 0 && r.data) f.id = r.data.id
    }
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    // 新建链接
    for (const link of f.links) {
      if (link._new && link.url) {
        const lr = await api('link_create', { app_id: f.id, pan_type: link.pan_type, label: link.label, url: link.url, password: link.password }, 'POST')
        if (lr.code !== 0 && lr.code !== 401) { Message.error('链接保存失败: ' + (lr.msg || '')); return }
        if (lr.code === 401) return
      }
    }
    showApp.value = false
    Message.success('保存成功')
    loadApps()
  } finally {
    saving.value = false
  }
}

/** 列表一键置顶 / 取消置顶 (只提交 id 与 is_top, 后端增量更新) */
async function toggleTop(record) {
  const next = Number(record.is_top) ? 0 : 1
  const r = await api('app_update', { id: record.id, is_top: next }, 'POST')
  if (r.code !== 0) { Message.error(r.msg || '操作失败'); return }
  record.is_top = next
  Message.success(next ? '已置顶' : '已取消置顶')
  loadApps()
}

async function delApp(app) {
  const r = await api('app_delete', { id: app.id }, 'POST')
  if (r.code !== 0 && r.code !== 401) { Message.error(r.msg || '删除失败'); return }
  Message.success('已删除')
  loadApps()
}

onMounted(loadAll)
</script>

<!-- 说明: .page-toolbar / .thumb 的通用样式定义在 styles/global.css, 此处不再重复, 以继承其手机端规则 -->
<style scoped>
.uc-box {
  border: 1px dashed var(--color-border-2);
  border-radius: 10px;
  padding: 12px;
  margin-bottom: 14px;
  background: var(--color-fill-1);
}
.uc-head { display: flex; align-items: center; gap: 6px; font-size: 13px; margin-bottom: 8px; flex-wrap: wrap; color: var(--color-text-1); }
.uc-sub { color: var(--color-text-3); font-size: 12px; }
.uc-err { color: rgb(var(--red-6)); font-size: 12px; margin-top: 8px; }
.uc-result { margin-top: 10px; }
.uc-suggest { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; font-size: 13px; margin-bottom: 8px; color: var(--color-text-1); }

/* 桌面端把操作按钮推到右侧; 手机端隐藏(按钮换行平分) */
.toolbar-spacer { flex: 1 1 auto; min-width: 0; }

.app-name { font-weight: 600; margin-right: 6px; }
.text-muted { color: var(--color-text-3); }

.upload-box {
  width: 96px; height: 96px; border: 1.5px dashed var(--color-border-2); border-radius: 8px;
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 4px; cursor: pointer; position: relative; overflow: hidden;
  color: var(--color-text-3); font-size: 12px;
  background: var(--color-fill-1);
}
.upload-box:hover { border-color: rgb(var(--primary-6)); color: rgb(var(--primary-6)); }
.upload-box-sm { width: 108px; height: 108px; flex-shrink: 0; }
.upload-img { width: 100%; height: 100%; object-fit: cover; }
.upload-plus { font-size: 24px; line-height: 1; }
.up-tip {
  position: absolute; inset: 0; background: rgba(0, 0, 0, .45); color: var(--color-white);
  display: flex; align-items: center; justify-content: center; font-size: 12px;
}
.up-txt { font-size: 11px; text-align: center; padding: 4px; line-height: 1.4; }

.shot-wrap { display: flex; flex-wrap: wrap; gap: 10px; }
.shot-item { position: relative; width: 108px; height: 108px; border-radius: 8px; overflow: hidden; border: 1.5px solid var(--color-border-2); }
.shot-img { width: 100%; height: 100%; object-fit: cover; }
.shot-del {
  position: absolute; top: 2px; right: 2px; background: rgb(var(--danger-6)); color: var(--color-white);
  width: 20px; height: 20px; border: none; border-radius: 50%; font-size: 12px;
  line-height: 20px; padding: 0; cursor: pointer;
}

.form-tip { font-size: 12px; color: var(--color-text-3); line-height: 1.6; margin: -4px 0 12px; }

.link-item {
  display: flex; align-items: center; gap: 8px; padding: 8px 10px;
  border: 1px solid var(--color-border-2); border-radius: 6px; margin-bottom: 8px;
}
.link-info { flex: 1; min-width: 0; font-size: 13px; }
.link-url { color: var(--color-text-3); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.link-editor { background: var(--color-fill-1); padding: 12px; border-radius: 6px; margin-bottom: 8px; }

/* 手机端微调: 工具栏按钮铺满、缩略图收紧、链接行可换行 */
@media (max-width: 820px) {
  .toolbar-spacer { display: none; }
  .upload-box { width: 84px; height: 84px; }
  .upload-box-sm, .shot-item { width: 92px; height: 92px; }
  .shot-wrap { gap: 8px; }
  .link-item { flex-wrap: wrap; row-gap: 6px; }
  .link-info { flex: 1 1 100%; }
  .link-item .arco-btn { flex: 1 1 auto; }
  .link-editor { padding: 10px; }
}

/* 手机端卡片补充: 文字缩略图居中 + 分页右对齐 (其余 .m-card-* 已在 global.css) */
.m-card-thumb-text { display: flex; align-items: center; justify-content: center; color: var(--color-text-3); font-size: 16px; font-weight: 600; }
.m-pager { justify-content: flex-end; margin-top: 12px; }
</style>
