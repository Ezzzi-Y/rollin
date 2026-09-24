(function(){
  var section = document.getElementById("offer");
  if(!section || !window.gsap) return;
  var gsap = window.gsap;
  var wrap = section.querySelector('[data-offer-reveal="wrap"]');
  var box  = section.querySelector('[data-offer-reveal="box"]');
  var line = section.querySelector('[data-offer-reveal="line"]');
  var poster = section.querySelector("[data-offer-poster]");
  var canvas = section.querySelector("[data-offer-canvas]");
  var flipEl = section.querySelector("[data-offer-flip]");
  if(!wrap || !box) return;

  /* 这个站没加载 CustomEase，自己解一条 cubic-bezier(.25,1,.5,1) */
  function bezier(x1,y1,x2,y2){
    function A(a,b){return 1-3*b+3*a}
    function B(a,b){return 3*b-6*a}
    function C(a){return 3*a}
    function calc(t,a,b){return ((A(a,b)*t+B(a,b))*t+C(a))*t}
    function slope(t,a,b){return 3*A(a,b)*t*t+2*B(a,b)*t+C(a)}
    return function(x){
      if(x<=0) return 0; if(x>=1) return 1;
      var t=x, i, d;
      for(i=0;i<8;i++){ d=calc(t,x1,x2)-x; if(Math.abs(d)<1e-5) return calc(t,y1,y2);
        var s=slope(t,x1,x2); if(Math.abs(s)<1e-6) break; t-=d/s; }
      var lo=0, hi=1; t=x;
      while(lo<hi){ d=calc(t,x1,x2); if(Math.abs(d-x)<1e-5) break;
        if(d>x) hi=t; else lo=t; t=(hi+lo)/2; if(hi-lo<1e-6) break; }
      return calc(t,y1,y2);
    };
  }
  var legendOut = bezier(.25,1,.5,1);

  /* ================== 录取数据：token → 姓名 / 方向 ==================
     后端公开端点（rollin，契约见 docs/rollin-api-inventory.md §7）：
       GET  <BASE>/api/public/offers/<token>            → { activity:{title}, candidateName,
                                                            status, effectiveStatus, actionable,
                                                            expiresAt, serverTime, successMessage? }
       POST <BASE>/api/public/offers/<token>/accept     → 200 { status:"ACCEPTED", ... }
       POST <BASE>/api/public/offers/<token>/decline    → 200 { status:"DECLINED", ... }
     注意：后端**没有** direction 字段，方向目前用 activity.title 顶（每个方向一个活动）。
     API_BASE 留空 = 还没接后端/同源部署，走下面的 DEMO_OFFERS 预览各状态：
       ?token=demo-dev（待确认）· demo-accepted · demo-declined · demo-expired · demo-inactive */
  var API_BASE = "";   // 例："https://t.example.edu.cn"，同源部署留空即可
  var DEMO_OFFERS = {
    "demo-dev":      { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "软件开发", effectiveStatus: "PENDING", actionable: true },
    "demo-algo":     { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "算法竞赛", effectiveStatus: "PENDING", actionable: true },
    "demo-sec":      { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "网络安全", effectiveStatus: "PENDING", actionable: true },
    "demo-ai":       { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "人工智能", effectiveStatus: "PENDING", actionable: true },
    "demo-vr":       { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "虚拟现实", effectiveStatus: "PENDING", actionable: true },
    "demo-accepted": { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "软件开发", effectiveStatus: "ACCEPTED", actionable: false,
                       successMessage: "欢迎加入软件开发方向，第一次例会的时间会在群里公布。" },
    "demo-declined": { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "软件开发", effectiveStatus: "DECLINED", actionable: false },
    "demo-expired":  { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "软件开发", effectiveStatus: "EXPIRED", actionable: false },
    "demo-inactive": { candidateName: "林知远", expiresAt: "2026-09-30T23:59:59+08:00", activityTitle: "软件开发", effectiveStatus: "INACTIVE", actionable: false }
  };

  var offerToken = (function(){
    var q = new URLSearchParams(location.search);
    var fromPath = (location.pathname.split("/o/")[1] || "").split("/")[0];
    return q.get("token") || q.get("t") || fromPath || "";
  })();

  function fmtDay(value){   // "2026-09-30" / ISO 串 → "2026 年 9 月 30 日"
    var d = value ? new Date(value) : new Date();
    if(isNaN(d.getTime())) d = new Date();
    return d.getFullYear() + " 年 " + (d.getMonth() + 1) + " 月 " + d.getDate() + " 日";
  }
  function fmtDeadline(value){   // RFC3339 → "2026 年 9 月 30 日 23:59"（本地时区）
    var d = value ? new Date(value) : new Date();
    if(isNaN(d.getTime())) d = new Date();
    function pad(n){ return String(n).padStart(2, "0"); }
    return fmtDay(value) + " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
  }
  function setField(field, value){
    if(value === undefined || value === null || value === "") return;
    var nodes = section.querySelectorAll('[data-offer-field="' + field + '"]');
    for(var i=0;i<nodes.length;i++) nodes[i].textContent = value;
  }
  function applyOffer(data){
    if(!data) return;
    var dir = data.direction && typeof data.direction === "object" ? data.direction.name : data.direction;
    setField("name", data.candidateName || data.name);
    /* 方向：后端暂时没有 direction 字段，用 activity.title 顶；哪天加了 direction 就直接生效 */
    var activity = data.activity && data.activity.title;
    setField("track", dir || activity || data.activityTitle || data.track);
    /* 截止日期：后端给的是 RFC3339（UTC），这里按本地时区显示成年月日 */
    setField("deadline", data.expiresAt ? fmtDeadline(data.expiresAt) : data.deadline);
  }
  /* 落款日期跟后端无关，永远取当天 */
  setField("date", fmtDay());

  /* ============ 状态机：待确认 / 已接受 / 已放弃 / 已过期 / 已失效 ============
     结果态 = 左栏居中 + 只留结果那一行；失效/放弃/过期把整张卡变灰。 */
  function finishCta(btn, kind, message){
    var main = section.querySelector(".offer-letter__confirm-main");
    var row = section.querySelector(".offer-letter__cta-row");
    var result = section.querySelector("[data-offer-result]");
    var letter = section.querySelector("[data-offer-letter]");
    if(btn){
      btn.classList.remove("is-busy");
      btn.classList.add(kind === "accept" ? "is-accepted" : "is-declined");
      btn.disabled = true;
    }
    if(row) row.classList.add("is-resolved");
    if(main) main.classList.add("is-resolved");
    if(result) result.textContent = message || (kind === "accept" ? "已接受，欢迎加入创新实验室！" : "已放弃本次录取资格。");
    if(kind === "accept"){
      fillAllBoxes();
    } else if(letter){
      letter.classList.add("is-muted");    // 放弃：整卡变灰
    }
  }

  function applyState(data){
    var st = String((data && (data.effectiveStatus || data.status)) || "PENDING").toUpperCase();
    var actionable = data ? (data.actionable !== false && st === "PENDING") : true;
    if(actionable) return;                  // 可操作：按钮保持可用，什么都不用做
    var acceptBtn = section.querySelector("[data-offer-accept]");
    var declineBtn = section.querySelector("[data-offer-decline]");
    if(st === "ACCEPTED"){ finishCta(acceptBtn, "accept", data.successMessage); return; }
    if(st === "DECLINED"){ finishCta(declineBtn, "decline", "你已放弃本次录取资格。"); return; }
    /* EXPIRED / INACTIVE / 链接无效：变灰 + 收起按钮 + 一行说明 */
    var letter = section.querySelector("[data-offer-letter]");
    var main = section.querySelector(".offer-letter__confirm-main");
    var row = section.querySelector(".offer-letter__cta-row");
    var result = section.querySelector("[data-offer-result]");
    if(letter) letter.classList.add("is-muted");
    if(main) main.classList.add("is-resolved");
    if(row) row.classList.add("is-resolved");
    if(result){
      result.textContent = st === "EXPIRED" ? "该 offer 已超过截止时间。" :
                           st === "INACTIVE" ? "该 offer 已失效。" : "链接无效或已失效。";
    }
  }

  /* ---------------- 接口层：三个公开端点 ----------------
     API_BASE 留空 = 同源（后端部署里 /api/public/* 和 /o/{token} 都在候选人域名下） */
  function offerUrl(action){
    return API_BASE + "/api/public/offers/" + encodeURIComponent(offerToken) + (action ? "/" + action : "");
  }
  /* 后端错误体是 { code, message, details }，把 code / details 挂到 Error 上，供状态映射用 */
  function readFailure(res){
    return res.text().then(function(txt){
      var body = null;
      try { body = JSON.parse(txt) } catch(e){ /* 非 JSON 错误体，用兜底文案 */ }
      var err = new Error((body && body.message) || ("HTTP " + res.status));
      err.code = (body && body.code) || ("HTTP_" + res.status);
      err.details = body && body.details;
      err.status = res.status;
      return err;
    });
  }
  function offerReq(action){
    var init = { credentials:"omit", headers:{ accept:"application/json" } };
    if(action) init.method = "POST";
    return fetch(offerUrl(action), init).then(function(res){
      if(!res.ok) return readFailure(res).then(function(err){ throw err });
      return res.json();
    });
  }
  /* 错误码 → 页面状态（04 §7 的错误矩阵）。返回 null 表示"不改状态，只提示" */
  function stateFromError(err){
    var code = err && err.code;
    if(code === "TOKEN_INVALID") return "INVALID";
    if(code === "ACTIVITY_DISABLED" || code === "ACTIVITY_ARCHIVED") return "INACTIVE";
    if(code === "OFFER_EXPIRED") return "EXPIRED";
    if(code === "OFFER_NOT_ACTIONABLE"){
      var cur = err.details && err.details.currentStatus;
      if(cur === "ACCEPTED") return "ACCEPTED";
      if(cur === "DECLINED") return "DECLINED";
      return "INACTIVE";   // 被其他方向录用作废等：统一按"已失效"展示
    }
    /* 错误体不是契约里的 JSON（比如被 nginx/网关拦下）时，按 HTTP 状态兜底 */
    var status = err && err.status;
    if(status === 404) return "INVALID";
    if(status === 410) return "EXPIRED";
    if(status === 403 || status === 409) return "INACTIVE";
    return null;           // RATE_LIMITED / 网络失败：状态不动，提示一句让用户重试
  }
  function showHint(msg){
    var hint = section.querySelector("[data-offer-hint]");
    if(!hint) return;
    hint.textContent = msg || "";
    if(msg) hint.classList.add("is-on"); else hint.classList.remove("is-on");
  }

  function loadOffer(){
    var demo = DEMO_OFFERS[offerToken];
    if(!offerToken || demo){          // 没 token / 本地演示 token：不请求后端
      if(demo){ applyOffer(demo); applyState(demo); }
      return Promise.resolve(demo || null);
    }
    return offerReq("")
      .then(function(data){ applyOffer(data); applyState(data); return data })
      .catch(function(err){
        console.warn("[offer] 拉取录取信息失败：", err);
        var st = stateFromError(err);
        if(st){ applyState({ effectiveStatus: st }); return null }
        showHint((err && err.message) || "暂时无法获取录取信息，请稍后重试。");
        return null;
      });
  }
  /* 接受 / 放弃：同一个形态，只差 URL 后缀；演示 token 走本地，不发请求 */
  function actionReq(action){
    if(!offerToken || DEMO_OFFERS[offerToken]) return Promise.resolve({ mock:true });
    return offerReq(action);
  }
  loadOffer();

  /* ============ 按钮：悬浮波浪（同 #sec-directions）+ 点击变对勾 ============
     站点自己的 ShapeOverlays 只挂在 header 里的 .js-color-button-fill 上，
     所以把同一套算法搬过来（点数、延迟、时长、配色都照抄），挂到 accept 按钮上。 */
  function ShapeOverlay(container, opts){
    opts = opts || {};
    var self = this;
    this.container = container;
    this.overlay = container.querySelector(".shape-overlays");
    if(!this.overlay) return;
    this.paths = this.overlay.querySelectorAll(".shape-overlays__path");
    this.colors = ["#EA3737","#004E9B","#FFB200","#FF5C38","#0072E3"];
    /* colorize=false 时不动路径颜色，交给 CSS 的 --ol-fill（卡片里的填充层用这个） */
    this.colorize = opts.colorize !== false;
    this.numPoints = 3;
    this.numPaths = this.paths.length;
    this.delayPointsMax = 0.2;
    this.delayPerPath = 0.2;
    this.duration = 0.3;
    this.isOpened = false;
    this.pointsDelay = [];
    this.allPoints = [];
    this.tl = gsap.timeline({ onUpdate: function(){ self.render() },
      defaults: { ease:"power2.inOut", duration:this.duration, delay:0 } });
    for(var i=0;i<this.numPaths;i++){
      var pts = [];
      for(var j=0;j<this.numPoints;j++) pts.push(100);
      this.allPoints.push(pts);
    }
    if(opts.hover !== false){
      this.onEnter = function(){ self.toggle(true) };
      this.onLeave = function(){ self.toggle(false) };
      container.addEventListener("mouseenter", this.onEnter);
      container.addEventListener("mouseleave", this.onLeave);
      container.addEventListener("touchstart", this.onEnter, { passive:true });
    }
    this.toggle(false, 0);
  }
  ShapeOverlay.prototype.toggle = function(open, dur){
    var duration = dur === undefined ? this.duration : dur;
    this.isOpened = !!open;
    if(open && this.colorize){
      gsap.utils.shuffle(this.colors);
      var pick = gsap.utils.selector(this.container);
      gsap.set(pick("._2"), { fill:this.colors[1] });
      gsap.set(pick("._3"), { fill:this.colors[2] });
    }
    this.tl.progress(0).clear();
    var i, j;
    for(i=0;i<this.numPoints;i++) this.pointsDelay[i] = Math.random() * this.delayPointsMax;
    for(i=0;i<this.numPaths;i++){
      var pts = this.allPoints[i];
      var lag = this.delayPerPath * (this.isOpened ? i : this.numPaths - i - 1);
      for(j=0;j<this.numPoints;j++){
        var from = {}, to = {};
        from[j] = 100;
        to[j] = 0;
        to.duration = duration;
        this.tl.fromTo(pts, from, to, this.pointsDelay[j] + lag);
      }
    }
  };
  ShapeOverlay.prototype.render = function(){
    for(var p=0;p<this.numPaths;p++){
      var pts = this.allPoints[p];
      var d = this.isOpened ? "M 0 0 V " + pts[0] + " C" : "M 0 " + pts[0] + " C";
      for(var o=0;o<this.numPoints-1;o++){
        var s = (o + 1) / (this.numPoints - 1) * 100;
        var a = s - 1 / (this.numPoints - 1) * 100 / 2;
        d += " " + a + " " + pts[o] + " " + a + " " + pts[o+1] + " " + s + " " + pts[o+1];
      }
      d += this.isOpened ? " V 100 H 0" : " V 0 H 0";
      this.paths[p].setAttribute("d", d);
    }
  };

  var cta = section.querySelector("[data-offer-accept]");
  var overlay = null;
  var fills = [];
  var reducedMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
  if(!reducedMotion){
    var fillNodes = section.querySelectorAll(".offer-letter__fill");
    for(var fi=0; fi<fillNodes.length; fi++){
      try { fills.push(new ShapeOverlay(fillNodes[fi], { hover:false, colorize:false })); }
      catch(err){ console.warn("[offer] 填充层初始化失败：", err); }
    }
  }

  /* 确认之后：从上到下依次把每块框用波浪补满颜色 */
  function fillAllBoxes(){
    var letter = section.querySelector("[data-offer-letter]");
    if(!fills.length){ if(letter) letter.classList.add("is-filled"); return; }
    for(var i=0; i<fills.length; i++){
      (function(ov, delay){
        setTimeout(function(){ ov.toggle(true); }, delay);
      })(fills[i], i * 90);
    }
    /* 文字颜色跟着波浪走：蓝底那条填到一半时由蓝转白 */
    setTimeout(function(){ if(letter) letter.classList.add("is-filled"); }, 340);
  }

  var noBtn = section.querySelector("[data-offer-decline]");
  /* 两个按钮都挂同一套悬浮波浪 */
  [cta, noBtn].forEach(function(btn){
    if(!btn || reducedMotion) return;
    try { var ov = new ShapeOverlay(btn); if(btn === cta) overlay = ov; }
    catch(err){ console.warn("[offer] 波浪初始化失败：", err); }
  });

  function failShake(btn, err, what){
    if(btn){
      btn.classList.remove("is-busy");
      btn.classList.add("is-error");
      setTimeout(function(){ btn.classList.remove("is-error"); }, 600);
    }
    console.warn("[offer] " + what + "失败：", err);
  }

  if(cta){
    cta.addEventListener("click", function(){
      if(cta.disabled || cta.classList.contains("is-accepted")) return;
      cta.classList.add("is-busy");
      actionReq("accept").then(function(res){
        showHint("");
        cta.setAttribute("aria-label", "已接受 offer");
        section.dataset.offerAccepted = "true";
        finishCta(cta, "accept", res && res.successMessage);
      }).catch(function(err){
        var st = stateFromError(err);                 // 后端已给出终态（如已被处理/已过期）：按终态渲染
        if(st){ applyState({ effectiveStatus: st }); return; }
        showHint((err && err.message) || "操作失败，请稍后重试。");
        failShake(cta, err, "接受 offer ");
      });
    });
  }

  /* 拒绝：防误点，第一次点先变成"再点一次确认"，3 秒内没再点就恢复 */
  if(noBtn){
    var armed = false, armedTimer = null;
    var label = noBtn.querySelector(".offer-letter__cta-label");
    var resetArm = function(){ armed = false; if(label) label.textContent = "拒绝 offer"; noBtn.classList.remove("is-armed"); };
    noBtn.addEventListener("click", function(){
      if(noBtn.disabled || noBtn.classList.contains("is-declined")) return;
      if(!armed){
        armed = true;
        if(label) label.textContent = "再点一次确认放弃";
        noBtn.classList.add("is-armed");
        armedTimer = setTimeout(resetArm, 3000);
        return;
      }
      if(armedTimer) clearTimeout(armedTimer);
      noBtn.classList.add("is-busy");
      actionReq("decline").then(function(){
        showHint("");
        noBtn.setAttribute("aria-label", "已放弃 offer");
        section.dataset.offerDeclined = "true";
        finishCta(noBtn, "decline");
      }).catch(function(err){
        resetArm();
        var st = stateFromError(err);
        if(st){ applyState({ effectiveStatus: st }); return; }
        showHint((err && err.message) || "操作失败，请稍后重试。");
        failShake(noBtn, err, "放弃 offer ");
      });
    });
  }

  window.OfferData = {
    token: function(){ return offerToken },
    reload: function(){ return loadOffer() },
    set: function(data){ applyOffer(data) },
    overlay: function(){ return overlay },   // 调试用：OfferData.overlay().toggle(true) 手动放按钮波浪
    fill: function(){ fillAllBoxes() }        // 调试用：直接看"确认后"的填色效果
  };


  /* 系统开了「减少动态效果」就不接管，方框直接是正常姿态 */
  if(matchMedia("(prefers-reduced-motion: reduce)").matches){
    /* 静态版：不做飞行、也不做翻转，直接正面朝上 */
    box.style.visibility = "visible";
    section.classList.add("is-settled");   // 压平卡片：正面朝上，且里面的按钮可以点
    section.classList.remove("is-flying");
    return;
  }

  box.style.visibility = "hidden";

  /* 时长：宽屏上同样的 1.2s 看起来"一闪而过"，按视口宽度略微放慢。
     想改手感只动这一处；移动端窄屏 k=1，保持原参数。 */
  var extra = 1;        // 额外的整体倍率，控制台里 LegendReveal.setFactor(2) 可改
  function factor(){
    var w = window.innerWidth || document.documentElement.clientWidth;
    return (w >= 1024 ? 1.35 : w >= 768 ? 1.15 : 1) * extra;
  }

  /* ===================== WebGL：把海报当一张会起伏的纸 =====================
     把海报贴到一张细分网格平面上，顶点着色器叠两条正弦波做 Z 向起伏，
     再用有限差分求法线、给一点明暗 —— 起伏才看得出来。相机是真透视，
     所以凸起来的部分会略微变大，看起来是"纸"而不是"平面在抖"。
     飞行结束就停掉画布、换回原来的 <img>，静止画面永远是锐利原图。
     不支持 WebGL 时整段跳过，只用 <img>，不会报错。 */
  /* 最大起伏幅度：平面半高 = 1，0.16 ≈ 半高的 16%（约 77px）。
     这就是"波浪大不大"的那个总开关，觉得太夸张往下调即可。 */
  var AMP = 0.16;
  var SEG_X = 20, SEG_Y = 28;   // 网格密度
  var gl = null, prog = null, uni = {}, mvpMat = new Float32Array(16), idxCount = 0;
  var wave = { amp: 0 }, rafId = null, t0 = 0;

  var VS = [
    "attribute vec2 aPos;",
    "uniform mat4 uMVP;",
    "uniform float uAmp;",
    "uniform float uTime;",
    "uniform float uHalfW;",
    "varying vec2 vUv;",
    "varying float vShade;",
    "float wave(vec2 p, float t){",
    "  return sin(p.x*2.2 + t*1.9)*0.5 + sin(p.y*1.7 - t*1.4 + p.x*0.9)*0.5;",
    "}",
    "void main(){",
    "  vUv = aPos;",
    "  float ex = aPos.x*2.0 - 1.0;",
    "  float ey = aPos.y*2.0 - 1.0;",
    "  vec2 p = vec2(ex*uHalfW, ey);",
    "  float edge = 0.30 + 0.70*max(abs(ex), abs(ey));",
    "  float z = wave(p, uTime)*uAmp*edge;",
    "  float e = 0.05;",
    "  float zx = wave(p + vec2(e,0.0), uTime)*uAmp*edge;",
    "  float zy = wave(p + vec2(0.0,e), uTime)*uAmp*edge;",
    "  vec3 n = normalize(vec3(-(zx-z)/e, -(zy-z)/e, 1.0));",
    "  vec3 L = normalize(vec3(-0.35, 0.55, 0.75));",
    "  vShade = 0.86 + 0.26*clamp(dot(n,L), 0.0, 1.0);",
    "  gl_Position = uMVP * vec4(p, z, 1.0);",
    "}"
  ].join("\n");

  var FS = [
    "precision mediump float;",
    "uniform sampler2D uTex;",
    "uniform vec2 uFit;",
    "varying vec2 vUv;",
    "varying float vShade;",
    "void main(){",
    "  /* 等比缩放到卡片里（和 CSS 的 object-fit:contain 同一套算法），外面补白 */",
    "  vec2 t = (vUv - 0.5) / uFit + 0.5;",
    "  vec2 inside = step(vec2(0.0), t) * step(t, vec2(1.0));",
    "  vec4 c = texture2D(uTex, clamp(t, 0.0, 1.0));",
    "  vec3 rgb = mix(vec3(1.0), c.rgb, inside.x * inside.y);",
    "  gl_FragColor = vec4(rgb*vShade, 1.0);",
    "}"
  ].join("\n");

  function shader(type, src){
    var s = gl.createShader(type);
    gl.shaderSource(s, src); gl.compileShader(s);
    if(!gl.getShaderParameter(s, gl.COMPILE_STATUS)){
      console.warn("[offer] 着色器编译失败:", gl.getShaderInfoLog(s)); return null;
    }
    return s;
  }

  function resizeGL(){
    if(!gl) return;
    var dpr = Math.min(window.devicePixelRatio || 1, 2);
    var w = Math.max(1, Math.round(box.clientWidth * dpr));
    var h = Math.max(1, Math.round(box.clientHeight * dpr));
    if(canvas.width !== w || canvas.height !== h){ canvas.width = w; canvas.height = h; }
    gl.viewport(0, 0, w, h);
    var aspect = box.clientWidth / Math.max(1, box.clientHeight);
    /* 不加 fit：平面半高 1、相机放在 d = 1/tan(22.5°) 处，平面正好铺满画布，
       而画布和盒子同尺寸 —— 所以 z=0（波浪摊平）时渲染出来的纸就等于原图尺寸，
       落地换回原图没有任何尺寸变化。
       代价：波浪峰值时投影外扩约 7%，超出画布的那一点点会被裁掉（在纸的最外侧）。 */
    var d = 1/Math.tan(Math.PI/8), f = d, near = 0.1, far = 50, nf = 1/(near-far);
    mvpMat[0]=f/aspect; mvpMat[1]=0; mvpMat[2]=0; mvpMat[3]=0;
    mvpMat[4]=0; mvpMat[5]=f; mvpMat[6]=0; mvpMat[7]=0;
    mvpMat[8]=0; mvpMat[9]=0; mvpMat[10]=(far+near)*nf; mvpMat[11]=-1;
    mvpMat[12]=0; mvpMat[13]=0;
    mvpMat[14]=2*far*near*nf - d*(far+near)*nf;
    mvpMat[15]=d;
    gl.uniform1f(uni.halfW, aspect);
    updateBackFit(aspect);
  }

  /* 卡背的"等比完整"比例：和 CSS 的 object-fit:contain 一致，
     顺便把余白处的纸纹格距/相位对齐到图片里那层格纹（图里格距 24px / 宽 1080）。 */
  var backFace = section.querySelector(".offer-face--back");
  var lastFit = "";
  function updateBackFit(aspect){
    var w = Math.max(1, box.clientWidth), h = Math.max(1, box.clientHeight);
    var sizeKey = w + "x" + h;
    var at = (poster && poster.naturalWidth) ? poster.naturalWidth / poster.naturalHeight : 1080 / 1528;
    var aq = aspect || (w / h);
    /* contain：卡片比图更"瘦"就按宽度对齐（图占满宽、上下留白），更"胖"就按高度对齐 */
    var fw = Math.min(1, at / aq), fh = Math.min(1, aq / at);
    /* uniform 每次都要推：WebGL 是后初始化的，之前那次调用时 gl 还不存在，
       如果这里跟着"尺寸没变"一起短路，uFit 会永远停在 (0,0)，整张纸被刷白。 */
    if(gl && uni.fit) gl.uniform2f(uni.fit, fw, fh);
    if(sizeKey === lastFit) return;   // 纸纹那几条样式只在尺寸变化时写
    lastFit = sizeKey;
    if(!backFace) return;
    var cell = 24 / 1080 * fw * w;
    backFace.style.setProperty("--paper-cell", cell.toFixed(2) + "px");
    backFace.style.setProperty("--paper-x", ((w - fw * w) / 2 + cell / 2).toFixed(2) + "px");
    backFace.style.setProperty("--paper-y", ((h - fh * h) / 2 + cell / 2).toFixed(2) + "px");
  }

  function setupGL(){
    if(!canvas || !poster || !poster.complete) return false;
    try { gl = canvas.getContext("webgl", {alpha:true, antialias:true}); } catch(e){ gl = null; }
    if(!gl) return false;
    var v = shader(gl.VERTEX_SHADER, VS), f = shader(gl.FRAGMENT_SHADER, FS);
    if(!v || !f) return false;
    prog = gl.createProgram();
    gl.attachShader(prog, v); gl.attachShader(prog, f); gl.linkProgram(prog);
    if(!gl.getProgramParameter(prog, gl.LINK_STATUS)){
      console.warn("[offer] 着色器链接失败:", gl.getProgramInfoLog(prog)); return false;
    }
    gl.useProgram(prog);
    uni.mvp = gl.getUniformLocation(prog, "uMVP");
    uni.amp = gl.getUniformLocation(prog, "uAmp");
    uni.time = gl.getUniformLocation(prog, "uTime");
    uni.halfW = gl.getUniformLocation(prog, "uHalfW");
    uni.tex = gl.getUniformLocation(prog, "uTex");
    uni.fit = gl.getUniformLocation(prog, "uFit");
    var aPos = gl.getAttribLocation(prog, "aPos");

    var verts = [], inds = [], x, y;
    for(y=0; y<=SEG_Y; y++){ for(x=0; x<=SEG_X; x++){ verts.push(x/SEG_X, y/SEG_Y); } }
    for(y=0; y<SEG_Y; y++){
      for(x=0; x<SEG_X; x++){
        var a = y*(SEG_X+1)+x, b = a+1, c = a+SEG_X+1, dd = c+1;
        inds.push(a,c,b, b,c,dd);
      }
    }
    idxCount = inds.length;
    var vbo = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, vbo);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(verts), gl.STATIC_DRAW);
    var ibo = gl.createBuffer();
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo);
    gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, new Uint16Array(inds), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(aPos);
    gl.vertexAttribPointer(aPos, 2, gl.FLOAT, false, 0, 0);

    /* 贴图就是这张海报本身（同源，不需要 CORS） */
    var tex = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, true);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    try { gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, poster); }
    catch(err){ console.warn("[offer] 贴图上传失败:", err); return false; }
    gl.uniform1i(uni.tex, 0);
    gl.disable(gl.DEPTH_TEST);
    gl.clearColor(0, 0, 0, 0);
    section.classList.add("is-webgl");
    resizeGL();
    return true;
  }

  function renderGL(now){
    if(!gl) return;
    gl.clear(gl.COLOR_BUFFER_BIT);
    gl.uniformMatrix4fv(uni.mvp, false, mvpMat);
    gl.uniform1f(uni.amp, wave.amp * AMP);
    gl.uniform1f(uni.time, (now - t0) / 1000);
    gl.drawElements(gl.TRIANGLES, idxCount, gl.UNSIGNED_SHORT, 0);
  }
  function loop(now){ renderGL(now); rafId = requestAnimationFrame(loop); }
  function startWave(){
    if(!gl) return;
    resizeGL();
    t0 = performance.now();
    if(rafId === null) rafId = requestAnimationFrame(loop);
  }
  function stopWave(){ if(rafId !== null){ cancelAnimationFrame(rafId); rafId = null; } }

  var tl = null, lineTween = null, lineDone = false, built = false;
  var flipTl = null;
  var state = "idle";   // idle 未播 / playing 正在飞 / shown 已停稳

  function build(){
    if(built) return;
    built = true;
    var k = factor();
    gsap.set(box, {transformPerspective:1200, visibility:"visible"});
    /* 传单飘过来：
       ① 主体保持原节的姿态（缩放 + Y 轴 -90° + Z 轴 -25° 收小一点），
          再给一点 xPercent / yPercent 位移，让它像从斜上方飘进来而不是原地长出来；
       ② 落定后再轻轻摆一下（rotate 1.3° → 0，scale 1.008 → 1），
          模仿一张纸落地时的那点回弹。不想要这半秒，删掉后面两个 .to 即可。 */
    tl = gsap.timeline({paused:true})
      /* 必须用 fromTo 把终点写死。
         用 .from() 的话，GSAP 会拿"元素当前的 transform"当终点值——
         而 build() 在 resize 时会重建，此刻纸很可能正停在复位后的倾斜姿态，
         于是终点被记成倾斜姿态，从此怎么播都转不回正面（就是"卡住"的那个 bug）。 */
      .fromTo(box,
        {scale:0.16, rotateY:-72, rotateX:-10, rotate:-18, xPercent:-8, yPercent:-14},
        {scale:1, rotateY:0, rotateX:0, rotate:0, xPercent:0, yPercent:0,
         duration:1.2*k, delay:0.2*k, ease:legendOut})
      .to(box, {rotate:1.3, scale:1.008, duration:0.32*k, ease:"sine.inOut"})
      .to(box, {rotate:0, scale:1, duration:0.38*k, ease:"sine.out"});
    /* 这里刻意不挂 onStart / onComplete：
       restart() / seek() 都会抑制回调，实测出现过"进度到 1 但 onComplete 没触发"，
       状态就永远卡在 playing。状态一律由 sync() 按进度判定。 */

    /* 起伏强度：先扬起来、中间缓一下、再落回 0（纸摊平） */
    tl.fromTo(wave, {amp:0}, {amp:1, duration:0.45*k, ease:"sine.out"}, 0)
      .to(wave, {amp:0.5, duration:0.5*k, ease:"sine.inOut"}, 0.45*k)
      .to(wave, {amp:1, duration:0.4*k, ease:"sine.inOut"}, 0.95*k)
      .to(wave, {amp:0, duration:0.6*k, ease:"sine.out"}, 1.35*k);

    /* 翻面：飞行结束后把卡片从背面转到正面。
       前 0.18 秒是留给眼睛的停顿（刚落地、还没翻），翻本身 0.9 秒。 */
    if(flipEl){
      flipTl = gsap.timeline({paused:true})
        .fromTo(flipEl, {rotateY:0}, {
          rotateY:180, duration:0.9*k, delay:0.18*k, ease:legendOut,
        });
      /* 翻面结束后收掉 WebGL 画布：那时背面已经转过去，
         画布→原图的交接发生在看不见的地方 */
      flipTl.eventCallback("onComplete", function(){
        stopWave();
        section.classList.remove("is-flying");
        section.classList.add("is-settled");   // 翻完就压平，恢复常规命中测试
      });
    }
    if(line){
      gsap.set(line, {clipPath:"inset(0% 0% -1px 100%)"});
      lineTween = gsap.fromTo(line,
        {clipPath:"inset(0% 100% -1px 0%)"},
        {clipPath:"inset(0% 0% -1px 0%)", duration:1.2*k, ease:legendOut, paused:true});
    }
  }

  function play(){
    build();
    tl.restart(true);
    if(flipTl) flipTl.pause(0);          // 每次入场都从背面开始
    if(lineTween && !lineDone){ lineDone = true; lineTween.restart(true); }
    state = "playing";
    section.classList.add("is-flying");
    startWave();
    section.dataset.offerDbg = "play@" + Math.round(window.scrollY);
  }

  /* 停稳：把画布收掉、换回锐利原图。由进度轮询调用，不依赖 onComplete */
  function settle(){
    if(state === "shown") return;
    state = "shown";
    /* 注意：这里**不**收掉画布，等翻面结束再收（见 flipTl.onComplete）——
       否则"画布→原图"的交接正好发生在翻面开始前，看起来就是卡一下 */
    if(flipTl && flipTl.progress() === 0) flipTl.restart(true);   // 落定 → 翻到正面
  }

  function rewind(){
    if(!tl) return;
    tl.pause(0);            // 退回起始姿态，但已经播过的分隔线不再收回
    if(flipTl) flipTl.pause(0);   // 卡片转回背面，下次入场重新翻
    state = "idle";
    stopWave();
    section.classList.remove("is-settled");   // 退回背面：重新变回 3D 卡片
    section.classList.remove("is-flying");
    section.dataset.offerDbg = "rewind@" + Math.round(window.scrollY);
  }

  /* 唯一的真相来源：**时间轴进度** + 是否在视口里。
     之前把状态交给 GSAP 的 onComplete 回调，实测会出现"进度已经到 1、
     但回调没触发"的情况（restart/seek 会吞事件），于是内部状态永远停在 playing，
     复位和看门狗都被它挡住 —— 就是用户看到的"卡住、转不回来"。
     现在改成每 250ms 对一次账，谁都不用信。 */
  function sync(){
    updateBackFit();    // 卡背的等比比例跟着盒子尺寸走（尺寸没变时内部直接返回）
    if(!built) return;
    if(!tl) return;
    var r = wrap.getBoundingClientRect();
    var vh = window.innerHeight || document.documentElement.clientHeight;
    var inView = r.bottom > 1 && r.top < vh - 1;
    var p = tl.progress();
    /* 调试用：把判定结果挂到 dataset 上，出问题时可以直接读 */
    section.dataset.offerIn = inView ? "in" : "out";
    section.dataset.offerTop = String(Math.round(r.top));
    section.dataset.offerState = state;
    section.dataset.offerP = p.toFixed(2);
    if(inView){
      if(p >= 1){
        settle();                       // 已经播完：确保画布收起、姿态是正面
      } else if(!tl.isActive()){
        play();                         // 没在跑也没跑完：播
      } else {
        state = "playing";
      }
    } else if(p > 0){
      rewind();
    }
  }

  var ticking = false;
  function queueSync(){
    if(ticking) return;
    ticking = true;
    requestAnimationFrame(function(){ ticking = false; sync(); });
  }

  addEventListener("scroll", queueSync, {passive:true});
  document.addEventListener("scroll", queueSync, {passive:true, capture:true});
  addEventListener("resize", function(){
    resizeGL();
    updateBackFit();        // 没有 WebGL 时 resizeGL 直接返回，纸纹也得跟着重算
    if(state === "idle"){ built = false; build(); }
    queueSync();
  });

  /* IntersectionObserver 兜底：即使某些滚动方式不冒泡出 scroll 事件也能触发 */
  if(window.IntersectionObserver){
    new IntersectionObserver(function(){ queueSync(); }, {threshold:[0, 0.25, 0.75, 1]}).observe(wrap);
  }

  /* 等字体和图片就绪再建（避免第一帧量歪），但加一个兜底定时器，
     万一 decode() 一直不 settle，也不会让方框永远藏着 */
  var booted = false;
  function boot(){
    if(booted) return;
    booted = true;
    setupGL();          // 失败也没关系：没有起伏，但原图照常显示
    updateBackFit();    // 卡背的等比比例 + 余白纸纹（WebGL 有没有都要算）
    build();
    sync();
    section.setAttribute("data-offer-ready","true");
  }
  var ready = [document.fonts.ready];
  Array.prototype.forEach.call(section.querySelectorAll("img"), function(img){
    ready.push(img.decode().catch(function(){}));
  });
  Promise.allSettled(ready).then(boot);
  setTimeout(boot, 3000);

  /* 定时对账：不管事件有没有触发、回调有没有被吞，每 250ms 纠正一次 */
  setInterval(sync, 250);

  /* 调试用：控制台 LegendReveal.replay() 重播；setFactor(2) 可以把飞行放慢一倍 */
  window.LegendReveal = {
    replay: function(){ built = false; build(); play(); },
    setFactor: function(v){ extra = v; built = false; build(); rewind(); check(); }
  };
})();
