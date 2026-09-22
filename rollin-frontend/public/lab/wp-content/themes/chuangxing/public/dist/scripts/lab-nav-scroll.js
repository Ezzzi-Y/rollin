(function(){
  function go(sel){
    var el = document.querySelector(sel);
    if(!el){ console.warn("[scroll] 未找到目标 " + sel); return; }
    var lenis = window.lenis;
    if(lenis && typeof lenis.scrollTo === "function"){ lenis.scrollTo(el, { duration: 1.2 }); }
    else { el.scrollIntoView({ behavior: "smooth", block: "start" }); }
  }
  document.addEventListener("click", function(e){
    var t = e.target;
    var el = t && t.closest && t.closest("[data-scroll-to],[data-offer-scroll]");
    if(!el) return;
    e.preventDefault();
    go(el.getAttribute("data-scroll-to") || "#offer");
  }, true);
})();
