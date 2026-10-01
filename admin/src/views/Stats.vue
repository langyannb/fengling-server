<template>
  <div class="stats-page">
    <a-row :gutter="[12, 12]">
      <a-col v-for="c in cards" :key="c.label" :xs="12" :sm="12" :md="6">
        <a-card class="stat-card" :bordered="false">
          <div class="stat-inner">
            <div class="stat-icon" :style="{ background: c.soft, color: c.color }">
              <component :is="c.icon" />
            </div>
            <div class="stat-text">
              <div class="stat-label">{{ c.label }}</div>
              <a-statistic
                :value="Number(c.value || 0)"
                :value-from="0"
                animation
                :style="{ color: c.color }"
              />
            </div>
          </div>
        </a-card>
      </a-col>
    </a-row>

    <a-card class="page-card" :bordered="false">
      <template #title>
        <span class="card-title">软件打开排行 (总榜)</span>
      </template>
      <template #extra>
        <a-button size="small" :loading="loading" @click="load">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </template>
      <a-table
        :data="topApps"
        :pagination="false"
        :scroll="{ x: 520 }"
        row-key="id"
        size="small"
      >
        <template #columns>
          <a-table-column title="#" :width="60">
            <template #cell="{ rowIndex }">
              <a-tag :color="rowIndex < 3 ? 'arcoblue' : 'gray'">{{ rowIndex + 1 }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="图标" :width="70">
            <template #cell="{ record }">
              <img v-if="record.icon" :src="record.icon" class="thumb" />
              <a-avatar v-else :size="40">{{ (record.name || '?')[0] }}</a-avatar>
            </template>
          </a-table-column>
          <a-table-column title="软件" data-index="name" />
          <a-table-column title="打开次数" :width="120">
            <template #cell="{ record }"><b class="num">{{ record.cnt }}</b> 次</template>
          </a-table-column>
        </template>
        <template #empty>暂无数据</template>
      </a-table>
    </a-card>

    <a-row :gutter="[12, 12]">
      <a-col :xs="24" :md="12">
        <a-card class="page-card" :bordered="false" title="今日打开排行">
          <a-table
            :data="topToday"
            :pagination="false"
            :scroll="{ x: 380 }"
            row-key="id"
            size="small"
          >
            <template #columns>
              <a-table-column title="#" :width="60">
                <template #cell="{ rowIndex }">{{ rowIndex + 1 }}</template>
              </a-table-column>
              <a-table-column title="软件" data-index="name" />
              <a-table-column title="次数" :width="90">
                <template #cell="{ record }"><b class="num">{{ record.cnt }}</b></template>
              </a-table-column>
            </template>
            <template #empty>今日暂无数据</template>
          </a-table>
        </a-card>
      </a-col>
      <a-col :xs="24" :md="12">
        <a-card class="page-card" :bordered="false" title="热门网盘链接">
          <a-table
            :data="topLinks"
            :pagination="false"
            :scroll="{ x: 360 }"
            row-key="id"
            size="small"
          >
            <template #columns>
              <a-table-column title="链接" data-index="label" />
              <a-table-column title="点击数" :width="100">
                <template #cell="{ record }"><b class="num">{{ record.cnt }}</b></template>
              </a-table-column>
            </template>
            <template #empty>暂无数据</template>
          </a-table>
        </a-card>
      </a-col>
    </a-row>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { IconApps, IconDownload, IconThunderbolt, IconCalendar } from '@arco-design/web-vue/es/icon'
import { api, pickList } from '../api'

const stats = ref({})
const loading = ref(false)

const cards = computed(() => [
  { label: '软件总数', value: stats.value.total_apps, color: '#165DFF', soft: 'rgba(22,93,255,.12)', icon: IconApps },
  { label: '总下载量', value: stats.value.total_downloads, color: '#00B42A', soft: 'rgba(0,180,42,.12)', icon: IconDownload },
  { label: '总打开', value: stats.value.total_clicks, color: '#FF7D00', soft: 'rgba(255,125,0,.12)', icon: IconThunderbolt },
  { label: '今日打开', value: stats.value.today_clicks, color: '#F53F3F', soft: 'rgba(245,63,63,.12)', icon: IconCalendar },
])
const topApps = computed(() => stats.value.top_apps || [])
const topToday = computed(() => stats.value.top_apps_today || [])
const topLinks = computed(() => stats.value.top_links || [])

async function load() {
  loading.value = true
  const r = await api('stats')
  loading.value = false
  if (r.code === 0) stats.value = r.data || {}
  else pickList(r)
}
onMounted(load)
</script>

<style scoped>
.stat-card {
  border-radius: 12px;
  height: 100%;
  transition: transform 0.16s ease, box-shadow 0.16s ease;
}
.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 6px 18px rgba(21, 45, 90, 0.08);
}
.stat-inner { display: flex; align-items: center; gap: 12px; }
.stat-icon {
  width: 42px;
  height: 42px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 20px;
  flex: 0 0 auto;
}
.stat-text { min-width: 0; }
.stat-label {
  font-size: 12px;
  color: var(--color-text-3);
  margin-bottom: 2px;
  white-space: nowrap;
}
.stat-text :deep(.arco-statistic-value) {
  font-size: 24px;
  font-weight: 700;
  line-height: 1.2;
}
.num { color: rgb(var(--primary-6)); font-size: 15px; }
.card-title { font-size: 15px; font-weight: 600; }

@media (max-width: 820px) {
  .stat-icon { width: 34px; height: 34px; font-size: 17px; border-radius: 10px; }
  .stat-inner { gap: 8px; }
  .stat-label { font-size: 11px; }
  .stat-text :deep(.arco-statistic-value) { font-size: 19px; }
  .stat-card :deep(.arco-card-body) { padding: 12px 10px; }
}
</style>
