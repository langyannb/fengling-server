<template>
  <div>
    <a-row :gutter="16">
      <a-col v-for="c in cards" :key="c.label" :xs="12" :md="6">
        <a-card class="page-card stat" :bordered="false">
          <a-statistic :value="Number(c.value || 0)" :title="c.label" :value-from="0" animation :style="{ color: c.color }" />
        </a-card>
      </a-col>
    </a-row>

    <a-card title="软件打开排行 (总榜)" class="page-card" :bordered="false">
      <template #extra><a-button size="small" @click="load"><template #icon><icon-refresh /></template>刷新</a-button></template>
      <a-table :data="topApps" :pagination="false" row-key="id" size="small">
        <template #columns>
          <a-table-column title="#" :width="60">
            <template #cell="{ rowIndex }"><a-tag :color="rowIndex < 3 ? 'arcoblue' : 'gray'">{{ rowIndex + 1 }}</a-tag></template>
          </a-table-column>
          <a-table-column title="图标" :width="70">
            <template #cell="{ record }">
              <img v-if="record.icon" :src="record.icon" class="thumb" />
              <a-avatar v-else :size="48">{{ (record.name || '?')[0] }}</a-avatar>
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

    <a-row :gutter="16">
      <a-col :xs="24" :md="12">
        <a-card title="今日打开排行" class="page-card" :bordered="false">
          <a-table :data="topToday" :pagination="false" row-key="id" size="small">
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
        <a-card title="热门网盘链接" class="page-card" :bordered="false">
          <a-table :data="topLinks" :pagination="false" row-key="id" size="small">
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
import { api, pickList } from '../api'

const stats = ref({})
const cards = computed(() => [
  { label: '软件总数', value: stats.value.total_apps, color: '#165DFF' },
  { label: '总下载量', value: stats.value.total_downloads, color: '#00B42A' },
  { label: '总打开', value: stats.value.total_clicks, color: '#FF7D00' },
  { label: '今日打开', value: stats.value.today_clicks, color: '#F53F3F' },
])
const topApps = computed(() => stats.value.top_apps || [])
const topToday = computed(() => stats.value.top_apps_today || [])
const topLinks = computed(() => stats.value.top_links || [])

async function load() {
  const r = await api('stats')
  if (r.code === 0) stats.value = r.data || {}
  else pickList(r)
}
onMounted(load)
</script>

<style scoped>
.stat :deep(.arco-statistic-value) { font-size: 26px; font-weight: 700; }
.num { color: rgb(var(--primary-6)); font-size: 15px; }
</style>
