<template>
  <a-menu
    class="side-menu"
    :selected-keys="selected"
    :default-open-keys="openKeys"
    @menu-item-click="onClick"
  >
    <a-menu-item key="stats">
      <template #icon><icon-dashboard /></template>
      数据统计
    </a-menu-item>

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
</template>

<script setup>
defineProps({
  selected: { type: Array, default: () => [] },
})
const emit = defineEmits(['select'])
const openKeys = ['g-apps', 'g-publish', 'g-settings']
function onClick(key) {
  emit('select', key)
}
</script>

<style scoped>
.side-menu {
  border: none;
  background: transparent;
}
</style>
