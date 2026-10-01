import { ref, computed } from 'vue'

/**
 * 全局响应式断点（模块级单例，只注册一次 resize 监听）
 *  - isMobile : <= 820px 手机 / 竖屏窄屏
 *  - isNarrow : <= 1200px 平板 / 小笔记本
 */
export const isMobile = ref(false)
export const isNarrow = ref(false)

function sync() {
  if (typeof window === 'undefined') return
  const w = window.innerWidth || document.documentElement.clientWidth || 0
  isMobile.value = w <= 820
  isNarrow.value = w <= 1200
}

if (typeof window !== 'undefined') {
  sync()
  window.addEventListener('resize', sync, { passive: true })
  window.addEventListener('orientationchange', sync, { passive: true })
}

/** 弹窗宽度：手机近全屏，桌面固定像素 */
export function modalWidth(desktop = 640) {
  return isMobile.value ? '94vw' : desktop + 'px'
}

/** 表格横向滚动宽度：手机给足空间，桌面自适应 */
export function tableScroll(desktopX) {
  return computed(() => (isMobile.value ? { x: Math.max(desktopX, 720) } : { x: desktopX }))
}

export function useResponsive() {
  return { isMobile, isNarrow, modalWidth, tableScroll }
}

export default useResponsive
