<template>
  <a-layout class="admin-layout">
    <a-layout-sider :width="210" :collapsed-width="56" collapsible breakpoint="lg">
      <div class="logo">
        <div class="logo-mark">风</div>
        <span v-show="!collapsed" class="logo-text">风铃分享库</span>
      </div>
      <a-menu :selected-keys="selectedKeys" :default-open-keys="openKeys" @menu-item-click="onMenuClick">
        <a-menu-item key="stats"><template #icon><icon-dashboard /></template>数据统计</a-menu-item>
        <a-sub-menu key="g-apps">
          <template #icon><icon-apps /></template>
          <template #title>内容管理</template>
          <a-menu-item key="apps">软件</a-menu-item>
          <a-menu-item key="cats">分类</a-menu-item>
          <a-menu-item key="banners">轮播图</a-menu-item>
        </a-sub-menu>
        <a-sub-menu key="g-publish">
          <template #icon><icon-cloud /></template>
          <template #title>发布</template>
          <a-menu-item key="version">版本发布</a-menu-item>
          <a-menu-item key="notice">公告设置</a-menu-item>
        </a-sub-menu>
        <a-sub-menu key="g-settings">
          <template #icon><icon-settings /></template>
          <template #title>运营</template>
          <a-menu-item key="contrib">投稿人</a-menu-item>
          <a-menu-item key="crash">崩溃日志</a-menu-item>
          <a-menu-item key="harm">和谐反馈</a-menu-item>
          <a-menu-item key="about">关于/联系方式</a-menu-item>
        </a-sub-menu>
      </a-menu>
    </a-layout-sider>

    <a-layout>
      <a-layout-header class="header">
        <div class="header-title">{{ route.meta.title || '' }}</div>
        <div class="header-right">
          <a-tag color="arcoblue"><icon-user /> {{ auth.user || 'admin' }}</a-tag>
          <a-button size="small" status="danger" @click="logout"><template #icon><icon-poweroff /></template>退出</a-button>
        </div>
      </a-layout-header>
      <a-layout-content class="content">
        <router-view v-slot="{ Component }">
          <component :is="Component" />
        </router-view>
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<script setup>
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Modal } from '@arco-design/web-vue'
import { auth } from '../api'

const route = useRoute()
const router = useRouter()
const collapsed = ref(false)
const selectedKeys = computed(() => [route.name])
const openKeys = ref(['g-apps', 'g-publish', 'g-settings'])

function onMenuClick(key) { router.push({ name: key }) }
function logout() {
  Modal.confirm({
    title: '确认退出登录?',
    content: '退出后需要重新输入账号密码',
    okText: '退出',
    cancelText: '取消',
    onOk: () => { auth.token = ''; auth.user = ''; router.push({ name: 'login' }) },
  })
}
</script>

<style scoped>
.admin-layout { height: 100vh; }
.logo { height: 56px; display: flex; align-items: center; gap: 8px; padding: 0 14px; border-bottom: 1px solid var(--color-border-2); }
.logo-mark { width: 26px; height: 26px; border-radius: 8px; background: rgb(var(--primary-6)); color: #fff; font-weight: 700; display: flex; align-items: center; justify-content: center; flex: 0 0 auto; }
.logo-text { font-weight: 600; white-space: nowrap; }
.header { height: 56px; display: flex; align-items: center; justify-content: space-between; background: var(--color-bg-2); border-bottom: 1px solid var(--color-border-2); padding: 0 16px; }
.header-title { font-size: 15px; font-weight: 600; }
.header-right { display: flex; align-items: center; gap: 10px; }
.content { padding: 16px; overflow: auto; background: var(--color-fill-1); }
</style>
