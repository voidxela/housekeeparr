// Progressive enhancement only: every page works without JavaScript.
(function () {
  "use strict";
  var main = document.getElementById("main");
  if (!main) return;
  var interval = parseInt(main.getAttribute("data-refresh") || "0", 10) * 1000;
  var loadedAt = Date.now();

  function label() {
    var el = document.querySelector("[data-updated]");
    if (!el) return;
    var s = Math.round((Date.now() - loadedAt) / 1000);
    el.textContent = s < 5 ? "updated just now" : "updated " + (s < 60 ? s + "s" : Math.round(s / 60) + " min") + " ago";
  }

  function busy() {
    var a = document.activeElement;
    if (a && a.closest && a.closest("form, details[open], select")) return true;
    if (document.querySelector("details[open]")) return true;
    return window.getSelection && String(window.getSelection()).length > 0;
  }

  // Refresh the main region from local state. Pages never trigger external
  // work, so refreshing is cheap and safe.
  function refresh() {
    if (document.hidden || busy()) return;
    fetch(window.location.href, { headers: { "Accept": "text/html" }, credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.text(); })
      .then(function (html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        var next = doc.getElementById("main");
        if (!next) return;
        main.innerHTML = next.innerHTML;
        var nav = doc.querySelector("header.side");
        var cur = document.querySelector("header.side");
        if (nav && cur) cur.innerHTML = nav.innerHTML;
        loadedAt = Date.now();
        label();
        main.classList.remove("is-offline");
      })
      .catch(function () {
        main.classList.add("is-offline");
        var el = document.querySelector("[data-updated]");
        if (el) el.textContent = "could not reach Housekeeparr; showing the last loaded page";
      });
  }

  label();
  setInterval(label, 5000);
  if (interval > 0) setInterval(refresh, interval);
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden && interval > 0 && Date.now() - loadedAt > interval) refresh();
  });
})();
