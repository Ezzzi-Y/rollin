/**
 * 实验室环境右侧"画廊"的色块墙。
 *
 * 主题原来往 .gallery-wrap 里挂一个 three.js 隧道（靠滚轮推进），容器类名已经换成
 * .lab-gallery，所以那套不再启动；这里用普通 DOM 色块接管，效果是：
 *   滚到视口里 → 各块从上下左右不同方向飘向自己的格位，途中沿运动方向拉长，落位回正。
 *   滚出视口 → 复位，回来可以重播。
 * 零依赖，只用 IntersectionObserver + CSS 动画（动画定义在 lab-gallery-style 里）。
 */
(function () {
  'use strict'

  var host = document.querySelector('[data-lab-gallery]')
  if (!host) return

  var COLORS = ['#0072e3', '#ffd633', '#41e277', '#ff5c38', '#7a5af8', '#00b2ff', '#ff8e0a', '#ea3737']
  var COUNT = 9
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches

  for (var i = 0; i < COUNT; i++) {
    var block = document.createElement('span')
    var extra = ''
    if (i === 2) extra = ' lab-gblock--wide'      // 一块占两列
    if (i === 4) extra = ' lab-gblock--tall'      // 一块占两行
    block.className = 'lab-gblock' + extra
    block.style.setProperty('--c', COLORS[i % COLORS.length])

    /* 方向：0 左 / 1 右 / 2 上 / 3 下 —— 距离是自身尺寸的 60%~115%，看起来才像"飘过来" */
    var dir = i % 4
    var dist = 60 + ((i * 37) % 55)
    var dx = dir === 0 ? -dist : (dir === 1 ? dist : 0)
    var dy = dir === 2 ? -dist : (dir === 3 ? dist : 0)
    block.style.setProperty('--dx', dx + '%')
    block.style.setProperty('--dy', dy + '%')

    /* 拉长：横向来的就横向拉长、纵向来的就竖向拉长；落位时回到 1:1 */
    var horizontal = (dir === 0 || dir === 1)
    block.style.setProperty('--sx', horizontal ? '1.45' : '.72')
    block.style.setProperty('--sy', horizontal ? '.72' : '1.45')
    block.style.setProperty('--rot', (((i % 3) - 1) * 6) + 'deg')
    block.style.setProperty('--dl', (i * 70) + 'ms')

    host.appendChild(block)
  }

  if (reduce || !('IntersectionObserver' in window)) {
    host.classList.add('is-play')
    return
  }

  var io = new IntersectionObserver(function (entries) {
    entries.forEach(function (entry) {
      if (entry.isIntersecting) host.classList.add('is-play')
      else host.classList.remove('is-play')      // 出去就复位，回来重播
    })
  }, { threshold: 0.18 })

  io.observe(host)
})()
