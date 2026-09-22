(function(){
  document.querySelectorAll(".lab-feed").forEach(function(root){
    var track = root.querySelector(".lab-feed__track");
    var prev = root.querySelector('[data-dir="-1"]');
    var next = root.querySelector('[data-dir="1"]');
    function step(){
      var card = track.querySelector(".lab-feed__card");
      if(!card) return 300;
      return card.getBoundingClientRect().width + 14;
    }
    prev.addEventListener("click", function(){ track.scrollBy({ left: -step(), behavior: "smooth" }); });
    next.addEventListener("click", function(){ track.scrollBy({ left: step(), behavior: "smooth" }); });
    function sync(){
      var max = track.scrollWidth - track.clientWidth - 2;
      prev.disabled = track.scrollLeft <= 2;
      next.disabled = track.scrollLeft >= max;
    }
    track.addEventListener("scroll", sync, { passive: true });
    window.addEventListener("resize", sync);
    sync();
  });
})();
