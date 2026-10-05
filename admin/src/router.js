import { createRouter, createWebHashHistory } from 'vue-router'
import { auth } from './api'

const routes = [
  { path: '/login', name: 'login', component: () => import('./views/Login.vue'), meta: { public: true, title: '登录' } },
  {
    path: '/',
    component: () => import('./layouts/BasicLayout.vue'),
    children: [
      { path: '', redirect: '/stats' },
      { path: 'stats', name: 'stats', component: () => import('./views/Stats.vue'), meta: { title: '数据统计', icon: 'IconDashboard' } },
      { path: 'apps', name: 'apps', component: () => import('./views/Apps.vue'), meta: { title: '软件管理', icon: 'IconApps', group: 'apps' } },
      { path: 'cats', name: 'cats', component: () => import('./views/Cats.vue'), meta: { title: '分类管理', icon: 'IconFolder', group: 'apps' } },
      { path: 'banners', name: 'banners', component: () => import('./views/Banners.vue'), meta: { title: '轮播图', icon: 'IconImage', group: 'apps' } },
      { path: 'version', name: 'version', component: () => import('./views/Version.vue'), meta: { title: '版本发布', icon: 'IconCloud', group: 'publish' } },
      { path: 'notice', name: 'notice', component: () => import('./views/Notice.vue'), meta: { title: '公告设置', icon: 'IconNotification', group: 'publish' } },
      { path: 'contrib', name: 'contrib', component: () => import('./views/Contrib.vue'), meta: { title: '投稿人', icon: 'IconUserGroup', group: 'settings' } },
      { path: 'crash', name: 'crash', component: () => import('./views/Crash.vue'), meta: { title: '崩溃日志', icon: 'IconBug', group: 'settings' } },
      { path: 'harm', name: 'harm', component: () => import('./views/Harm.vue'), meta: { title: '和谐反馈', icon: 'IconExclamationCircle', group: 'settings' } },
      { path: 'about', name: 'about', component: () => import('./views/About.vue'), meta: { title: '关于/联系方式', icon: 'IconSettings', group: 'settings' } },
      { path: 'uc', name: 'uc', component: () => import('./views/UcAccount.vue'), meta: { title: 'UC 网盘', icon: 'IconLink', group: 'settings' } },
      { path: 'users', name: 'users', component: () => import('./views/Users.vue'), meta: { title: '用户管理', icon: 'IconUser', group: 'settings' } },
      { path: 'groups', name: 'groups', component: () => import('./views/Groups.vue'), meta: { title: '群组管理', icon: 'IconUserGroup', group: 'social' } },
      { path: 'social', name: 'social', component: () => import('./views/Social.vue'), meta: { title: '群消息', icon: 'IconMessage', group: 'social' } },
      { path: 'notify', name: 'notify', component: () => import('./views/Notify.vue'), meta: { title: '通知下发', icon: 'IconNotification', group: 'social' } },
      { path: 'pm', name: 'pm', component: () => import('./views/Pm.vue'), meta: { title: '私聊管理', icon: 'IconMessage', group: 'social' } },
      { path: 'lottery', name: 'lottery', component: () => import('./views/Lottery.vue'), meta: { title: '抽奖管理', icon: 'IconGift', group: 'settings' } },
      { path: 'video', name: 'video', component: () => import('./views/Video.vue'), meta: { title: '视频消息', icon: 'IconVideoCamera', group: 'settings' } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/stats' },
]

export const router = createRouter({ history: createWebHashHistory(), routes })

router.beforeEach((to) => {
  if (!to.meta.public && !auth.token) return { name: 'login' }
  if (to.name === 'login' && auth.token) return { name: 'stats' }
  return true
})

export default router
