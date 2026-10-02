<template>
  <a-layout class="admin-layout">
    <!-- 桌面端固定侧栏 -->
    <a-layout-sider
      v-if="!isMobile"
      class="side"
      :width="216"
      :collapsed-width="60"
      collapsible
      :collapsed="collapsed"
      @collapse="onCollapse"
    >
      <div class="logo" :class="{ mini: collapsed }">
        <div class="logo-mark">风</div>
        <span v-show="!collapsed" class="logo-text">风铃分享库</span>
      </div>
      <div class="menu-wrap">
        <SideMenu :selected="selectedKeys" @select="go" />
      </div>
      <div v-show="!collapsed" class="side-foot">v1.0 · 管理后台</div>
    </a-layout-sider>

    <a-layout class="main">
      <a-layout-header class="header">
        <a-button v-if="isMobile" class="hamburger" type="text" @click="drawer = true">
          <template #icon><icon-menu /></template>
        </a-button>
        <div v-else class="header-mark" />
        <div class="header-title">{{ route.meta.title || '管理后台' }}</div>
        <div class="header-right">
          <a-tag :bordered="false" color="arcoblue" class="user-tag">
            <template #icon><icon-user /></template>
            <span class="user-name">{{ auth.user || 'admin' }}</span>
          </a-tag>
          <a-button size="small" status="danger" @click="logout">
            <template #icon><icon-poweroff /></template>
            <span v-if="!isMobile">退出</span>
          </a-button>
        </div>
      </a-layout-header>

      <a-layout-content ref="contentRef" class="content">
        <router-view v-slot="{ Component }">
          <component :is="Component" />
        </router-view>
      </a-layout-content>
    </a-layout>

    <!-- 手机端抽屉菜单 -->
    <a-drawer
      v-model:visible="drawer"
      class="mobile-drawer"
      placement="left"
      :width="drawerWidth"
      :footer="false"
      :header="false"
      unmount-on-close
    >
      <div class="logo">
        <div class="logo-mark">风</div>
        <span class="logo-text">风铃分享库</span>
      </div>
      <SideMenu :selected="selectedKeys" @select="go" />
    </a-drawer>
  </a-layout>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Modal } from '@arco-design/web-vue'
import { auth } from '../api'
import SideMenu from '../components/SideMenu.vue'
import { isMobile } from '../composables/useResponsive'

const route = useRoute()
const router = useRouter()
const collapsed = ref(false)
const drawer = ref(false)
// 切页时把内容区滚回顶部(手机端尤其明显)
const contentRef = ref(null)
const selectedKeys = computed(() => [route.name])
const drawerWidth = computed(() => {
  const w = (typeof window !== 'undefined' && window.innerWidth) || 360
  return Math.min(272, Math.max(208, Math.round(w * 0.78)))
})

function go(key) {
  router.push({ name: key })
}
function onCollapse(v) {
  collapsed.value = v
}
watch(
  () => route.fullPath,
  () => {
    drawer.value = false
    if (contentRef.value) contentRef.value.scrollTop = 0
  }
)
watch(isMobile, (v) => { if (!v) drawer.value = false })

function logout() {
  Modal.confirm({
    title: '确认退出登录?',
    content: '退出后需要重新输入账号密码',
    okText: '退出',
    cancelText: '取消',
    onOk: () => {
      auth.token = ''
      auth.user = ''
      router.push({ name: 'login' })
    },
  })
}
</script>

<style scoped>
.admin-layout {
  height: 100vh;
  height: 100dvh;
}
.side {
  background: var(--color-bg-2);
  border-right: 1px solid var(--color-border-2);
}
.side :deep(.arco-layout-sider-children) {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.logo {
  height: 56px;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 16px;
  flex: 0 0 auto;
}
.logo.mini {
  padding: 0 14px;
  justify-content: center;
}
.logo-mark {
  width: 28px;
  height: 28px;
  border-radius: 9px;
  background: linear-gradient(135deg, rgb(var(--primary-5)), rgb(var(--primary-7)));
  color: #fff;
  font-weight: 700;
  font-size: 15px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  box-shadow: 0 2px 6px rgba(var(--primary-6), 0.28);
}
.logo-text {
  font-weight: 600;
  font-size: 15px;
  white-space: nowrap;
}
.menu-wrap {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 4px 8px;
}
.side-foot {
  flex: 0 0 auto;
  padding: 10px 16px 14px;
  font-size: 12px;
  color: var(--color-text-3);
}
.main {
  min-width: 0;
}
.header {
  height: 56px;
  line-height: 56px;
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--color-bg-2);
  border-bottom: 1px solid var(--color-border-2);
  padding: 0 14px;
  position: sticky;
  top: 0;
  z-index: 20;
}
.hamburger {
  font-size: 18px;
  padding: 0 8px;
  margin-left: -6px;
}
.header-mark {
  width: 2px;
}
.header-title {
  font-size: 15px;
  font-weight: 600;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.header-right {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
}
.user-tag {
  display: inline-flex;
  align-items: center;
  max-width: 34vw;
}
.user-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.content {
  padding: 16px;
  overflow: auto;
  background: var(--color-fill-1);
  -webkit-overflow-scrolling: touch;
}
@media (max-width: 820px) {
  .content {
    padding: 10px;
    padding-bottom: calc(20px + env(safe-area-inset-bottom, 0px));
  }
  .header {
    padding: 0 10px;
  }
}
.mobile-drawer :deep(.arco-drawer-content) {
  padding: 0 8px 12px;
}
.mobile-drawer .logo {
  padding: 12px 8px 10px;
}
</style>
