function syntaxHighlight(json) {
  json = JSON.stringify(json, undefined, 2);
  json = json
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
  if (json.length > 1000000) {
    return `<span class="key">${json}</span>`;
  }
  return json.replace(
    /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g,
    (match) => {
      let cls = "number";
      if (/^"/.test(match)) {
        if (/:$/.test(match)) {
          cls = "key";
        } else {
          cls = "string";
        }
      } else if (/true|false/.test(match)) {
        cls = "boolean";
      } else if (/null/.test(match)) {
        cls = "null";
      }
      return `<span class="${cls}">${match}</span>`;
    }
  );
}

function readCoinCookie() {
  return document.cookie
    .split("; ")
    .find((row) => row.startsWith("secondary_coin="))
    ?.split("=");
}

// The server sets secondary_coin only once the visitor picked a currency, so pages without it
// stay cacheable. Without the cookie the page shows the default currency: create the cookie
// here, in the server's format ("secondary_coin=USD=false", session cookie, Path=/).
function getCoinCookie() {
  if (!hasSecondary) return undefined;
  let a = readCoinCookie();
  if (!a && typeof secondaryCoinDefault === "string") {
    document.cookie = `secondary_coin=${secondaryCoinDefault}; Path=/`;
    // cookies blocked: still use the default, so the tooltips and the switch work on this page
    a = readCoinCookie() || ["secondary_coin", ...secondaryCoinDefault.split("=")];
  }
  return a;
}

function changeCSSStyle(selector, cssProp, cssVal) {
  const mIndex = 1;
  const cssRules = document.all ? "rules" : "cssRules";
  for (
    i = 0, len = document.styleSheets[mIndex][cssRules].length;
    i < len;
    i++
  ) {
    if (document.styleSheets[mIndex][cssRules][i].selectorText === selector) {
      document.styleSheets[mIndex][cssRules][i].style[cssProp] = cssVal;
      return;
    }
  }
}

function amountTooltip() {
  const prim = this.querySelector(".prim-amt");
  const sec = this.querySelector(".sec-amt");
  const csec = this.querySelector(".csec-amt");
  const base = this.querySelector(".base-amt");
  const cbase = this.querySelector(".cbase-amt");
  let s = `${prim.outerHTML}<br>`;
  if (base) {
    let t = base.getAttribute("tm");
    if (!t) {
      t = "now";
    }
    s += `<span class="amt-time">${t}</span>${base.outerHTML}<br>`;
  }
  if (cbase) {
    s += `<span class="amt-time">now</span>${cbase.outerHTML}<br>`;
  }
  if (sec) {
    let t = sec.getAttribute("tm");
    if (!t) {
      t = "now";
    }
    s += `<span class="amt-time">${t}</span>${sec.outerHTML}<br>`;
  }
  if (csec) {
    s += `<span class="amt-time">now</span>${csec.outerHTML}<br>`;
  }
  return `<span class="l-tooltip">${s}</span>`;
}

function addressAliasTooltip() {
  const type = this.getAttribute("alias-type");
  const address = this.getAttribute("cc");
  return `<span class="l-tooltip">${type}<br>${address}</span>`;
}

function handleTxPage(rawData, txId) {
  const rawOutput = document.getElementById('raw');
  const rawButton = document.getElementById('raw-button');
  const rawHexButton = document.getElementById('raw-hex-button');

  rawOutput.innerHTML = syntaxHighlight(rawData);

  let isShowingHexData = false;

  const memoizedResponses = {};

  async function getTransactionHex(txId) {
      // BTC-like coins have a 'hex' field in the raw data
      if (rawData['hex']) {
          return rawData['hex'];
      }
      if (memoizedResponses[txId]) {
          return memoizedResponses[txId];
      }
      const fetchedData = await fetchTransactionHex(txId);
      memoizedResponses[txId] = fetchedData;
      return fetchedData;
  }

  async function fetchTransactionHex(txId) {
      const response = await fetch(`/api/rawtx/${txId}`);
      if (!response.ok) {
          throw new Error(`Error fetching data: ${response.status}`);
      }
      const txHex = await response.text();
      const hexWithoutQuotes = txHex.replace(/"/g, '');
      return hexWithoutQuotes;
  }

  function updateButtonStyles() {
      if (isShowingHexData) {
          rawButton.classList.add('active');
          rawButton.style.fontWeight = 'normal';
          rawHexButton.classList.remove('active');
          rawHexButton.style.fontWeight = 'bold';
      } else {
          rawButton.classList.remove('active');
          rawButton.style.fontWeight = 'bold';
          rawHexButton.classList.add('active');
          rawHexButton.style.fontWeight = 'normal';
      }
  }

  updateButtonStyles();

  rawHexButton.addEventListener('click', async () => {
      if (!isShowingHexData) {
          try {
              const txHex = await getTransactionHex(txId);
              rawOutput.textContent = txHex;
          } catch (error) {
              console.error('Error fetching raw transaction hex:', error);
              rawOutput.textContent = `Error fetching raw transaction hex: ${error.message}`;
          }
          isShowingHexData = true;
          updateButtonStyles();
      }
  });

  rawButton.addEventListener('click', () => {
      if (isShowingHexData) {
          rawOutput.innerHTML = syntaxHighlight(rawData);
          isShowingHexData = false;
          updateButtonStyles();
      }
  });
}

// Home page product strip (.hub-products-strip). Without this script it is a plain sideways
// scrolling row. Only while its tiles do not fit (re-checked on resize), it becomes a slow
// carousel. Every product appears once: there are no copies of the tiles.
// - the tiles drift at DRIFT px/s by advancing scrollLeft, so drift, swipe, wheel, drag, keys
//   and buttons all move one scroll position. The drift runs to the end, holds HOLD ms, runs
//   back to the start, holds, and so on.
// - the drift pauses while the mouse is over the strip, keyboard focus is inside it, a pointer
//   or touch is down, the tab is hidden or the strip is off-screen, and resumes RESUME ms after
//   the last interaction. prefers-reduced-motion: reduce stops it and smooth scrolling.
// - a mouse can drag the row (a drag never opens a tile link); the ‹ › buttons and
//   ArrowLeft/ArrowRight move one tile, from the last tile on to the first and back;
//   Home/End go to the first/last tile. The keys also work while the mouse is over the strip.
// - over the strip a vertical mouse wheel scrolls it sideways until it reaches an end; there
//   the wheel scrolls the page again.
// Scroll snapping is switched off in carousel mode (hub.css): it would fight the drift.
function hubProductCarousel(strip) {
  const DRIFT = 25;
  const HOLD = 2500;
  const RESUME = 4000;
  const DRAG = 5;
  const real = Array.from(strip.children).filter((e) => e.classList.contains("hub-product"));
  if (real.length < 2) return;
  const n = real.length;
  const reduce = window.matchMedia("(prefers-reduced-motion: reduce)");

  const view = document.createElement("div");
  view.className = "hub-products-viewport";
  strip.parentNode.insertBefore(view, strip);
  view.appendChild(strip);
  const button = (cls, label, glyph, dir) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = `hub-products-nav ${cls}`;
    b.setAttribute("aria-label", label);
    b.hidden = true;
    const g = document.createElement("span");
    g.setAttribute("aria-hidden", "true");
    g.textContent = glyph;
    b.appendChild(g);
    b.addEventListener("click", () => {
      touched();
      step(dir);
    });
    return b;
  };
  const prev = button("hub-products-prev", "Previous products", "‹", -1);
  const next = button("hub-products-next", "Next products", "›", 1);
  // On touch / coarse pointers the buttons live in .hub-products-nav-pair, in the title row of the
  // card, so they never sit on a tile (hub.css). Otherwise they flank the strip. The pair comes
  // before the strip in the DOM, so Tab order follows the visual order.
  const grid = view.parentNode;
  const pair = document.createElement("div");
  pair.className = "hub-products-nav-pair";
  const touch = window.matchMedia("(hover: none), (pointer: coarse)");
  const place = () => {
    if (touch.matches) {
      pair.append(prev, next);
      if (pair.parentNode !== grid) grid.insertBefore(pair, view);
    } else {
      view.insertBefore(prev, strip);
      view.appendChild(next);
      pair.remove();
    }
  };
  place();
  if (touch.addEventListener) touch.addEventListener("change", place);
  else if (touch.addListener) touch.addListener(place);

  let active = false;
  let tileW = 0; // from one tile to the next
  let pos = 0; // sub-pixel scroll position while drifting
  let dir = 1; // drift direction: 1 toward the end, -1 back to the start
  let holdUntil = 0; // rAF time until which the drift rests at an end
  let raf = 0;
  let last = 0;
  let resumeAt = 0;
  let resumeTimer = 0;
  let hover = false;
  let keyFocus = false;
  let mouseDown = false;
  let touchDown = false;
  let onScreen = true;

  const maxScroll = () => Math.max(0, strip.scrollWidth - strip.clientWidth);
  const atStart = () => strip.scrollLeft <= 1;
  const atEnd = () => strip.scrollLeft >= maxScroll() - 1;
  const glide = (x) => {
    strip.scrollTo({ left: Math.max(0, Math.min(x, maxScroll())), behavior: reduce.matches ? "auto" : "smooth" });
  };
  const measure = () => {
    tileW = real[1].getBoundingClientRect().left - real[0].getBoundingClientRect().left;
  };
  const overflows = () => {
    const cs = getComputedStyle(strip);
    const box = strip.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    return real[n - 1].getBoundingClientRect().right - real[0].getBoundingClientRect().left > box + 1;
  };

  const evaluate = () => {
    const want = overflows();
    if (want === active) {
      if (active) measure();
      return;
    }
    active = want;
    if (active) {
      strip.classList.add("hub-products-carousel");
      measure();
    } else {
      strip.classList.remove("hub-products-carousel", "hub-products-dragging");
    }
    strip.scrollLeft = 0;
    dir = 1;
    prev.hidden = next.hidden = !active;
    grid.classList.toggle("hub-products-nav-on", active);
    update();
  };

  const canRun = () =>
    active && !reduce.matches && !hover && !keyFocus && !mouseDown && !touchDown && onScreen && !document.hidden && Date.now() >= resumeAt;
  const frame = (now) => {
    raf = 0;
    if (!canRun()) return;
    const dt = last ? Math.min(now - last, 100) : 0;
    last = now;
    if (now >= holdUntil) {
      const max = maxScroll();
      pos += (dir * DRIFT * dt) / 1000;
      if (pos >= max || pos <= 0) {
        pos = pos >= max ? max : 0;
        dir = pos > 0 ? -1 : 1;
        holdUntil = now + HOLD;
      }
      strip.scrollLeft = pos;
    }
    raf = requestAnimationFrame(frame);
  };
  function update() {
    clearTimeout(resumeTimer);
    if (canRun()) {
      if (!raf) {
        pos = strip.scrollLeft;
        last = 0;
        holdUntil = 0;
        raf = requestAnimationFrame(frame);
      }
      return;
    }
    if (raf) {
      cancelAnimationFrame(raf);
      raf = 0;
    }
    const wait = resumeAt - Date.now();
    if (active && wait > 0) resumeTimer = setTimeout(update, wait + 20);
  }
  // an interaction happened or ended: drift resumes RESUME ms from now
  function touched() {
    resumeAt = Date.now() + RESUME;
    update();
  }

  // one tile forward or back from the tile at the left edge; past the last tile back to the
  // first, and before the first on to the last, so the buttons never dead-end
  function step(d) {
    if (!active) return;
    const x = strip.scrollLeft;
    if (d > 0 && atEnd()) glide(0);
    else if (d < 0 && atStart()) glide(maxScroll());
    else if (d > 0) glide((Math.floor(x / tileW + 0.02) + 1) * tileW);
    else glide((Math.ceil(x / tileW - 0.02) - 1) * tileW);
  }
  // to tile i (0..n-1)
  const goTo = (i) => glide(i * tileW);

  view.addEventListener("pointerenter", (e) => {
    if (e.pointerType === "mouse") {
      hover = true;
      update();
    }
  });
  view.addEventListener("pointerleave", (e) => {
    if (e.pointerType === "mouse") {
      hover = false;
      touched();
    }
  });
  // keyboard focus only: a mouse click also focuses the region or a tile link
  const onFocusIn = (e) => {
    let visible = true;
    try {
      visible = e.target.matches(":focus-visible");
    } catch (err) {}
    if (visible) {
      keyFocus = true;
      update();
    }
  };
  const onFocusOut = (e) => {
    if (!view.contains(e.relatedTarget) && !pair.contains(e.relatedTarget)) {
      keyFocus = false;
      touched();
    }
  };
  [view, pair].forEach((el) => {
    el.addEventListener("focusin", onFocusIn);
    el.addEventListener("focusout", onFocusOut);
  });
  // a vertical wheel scrolls the strip sideways while it can still move that way; at an end the
  // event goes through and the page scrolls. Horizontal wheels and trackpads stay native.
  view.addEventListener(
    "wheel",
    (e) => {
      touched();
      if (!active || e.ctrlKey || Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
      const px = e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? strip.clientWidth : 1);
      if ((px > 0 && atEnd()) || (px < 0 && atStart())) return;
      e.preventDefault();
      strip.scrollLeft += px;
    },
    { passive: false }
  );
  view.addEventListener(
    "touchstart",
    () => {
      touchDown = true;
      update();
    },
    { passive: true }
  );
  const touchEnd = (e) => {
    if (e.touches.length) return;
    touchDown = false;
    touched();
  };
  view.addEventListener("touchend", touchEnd, { passive: true });
  view.addEventListener("touchcancel", touchEnd, { passive: true });

  // mouse drag; touch and pen keep native panning
  let drag = null;
  let suppressClick = false;
  strip.addEventListener("pointerdown", (e) => {
    if (e.pointerType !== "mouse" || e.button !== 0 || !active) return;
    mouseDown = true;
    update();
    drag = { id: e.pointerId, x: e.clientX, left: strip.scrollLeft, moved: false };
  });
  strip.addEventListener("pointermove", (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    const dx = e.clientX - drag.x;
    if (!drag.moved) {
      if (Math.abs(dx) <= DRAG) return;
      drag.moved = true;
      strip.classList.add("hub-products-dragging");
      try {
        strip.setPointerCapture(e.pointerId);
      } catch (err) {}
    }
    strip.scrollLeft = Math.max(0, Math.min(drag.left - dx, maxScroll()));
  });
  const endPointer = (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    if (drag.moved) {
      suppressClick = true;
      setTimeout(() => (suppressClick = false), 0);
      strip.classList.remove("hub-products-dragging");
    }
    drag = null;
    mouseDown = false;
    touched();
  };
  window.addEventListener("pointerup", endPointer);
  window.addEventListener("pointercancel", endPointer);
  // a drag must not open the tile it started on
  strip.addEventListener(
    "click",
    (e) => {
      if (suppressClick) {
        suppressClick = false;
        e.preventDefault();
        e.stopPropagation();
      }
    },
    true
  );
  // links and images are natively draggable, which would end the mouse drag
  strip.addEventListener("dragstart", (e) => {
    if (active) e.preventDefault();
  });

  const isMoveKey = (e) =>
    !e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && ["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key);
  strip.addEventListener("keydown", (e) => {
    if (!active || !isMoveKey(e)) return;
    const k = e.key;
    e.preventDefault();
    keyFocus = true;
    touched();
    const tile = e.target.closest(".hub-product");
    const at = real.indexOf(tile);
    if (at < 0) {
      if (k === "Home" || k === "End") goTo(k === "Home" ? 0 : n - 1);
      else step(k === "ArrowRight" ? 1 : -1);
      return;
    }
    let i;
    if (k === "Home") i = 0;
    else if (k === "End") i = n - 1;
    else i = (at + (k === "ArrowRight" ? 1 : n - 1)) % n;
    real[i].focus({ preventScroll: true });
    goTo(i);
  });
  // with the mouse over the strip the same keys work without focus inside it, unless the user
  // is typing somewhere
  document.addEventListener("keydown", (e) => {
    if (!active || !hover || e.defaultPrevented || !isMoveKey(e)) return;
    const t = e.target;
    if (t && (t.isContentEditable || (t.closest && t.closest("input, textarea, select, [contenteditable]")))) return;
    e.preventDefault();
    touched();
    if (e.key === "Home") glide(0);
    else if (e.key === "End") glide(maxScroll());
    else step(e.key === "ArrowRight" ? 1 : -1);
  });

  document.addEventListener("visibilitychange", update);
  if (reduce.addEventListener) reduce.addEventListener("change", update);
  else if (reduce.addListener) reduce.addListener(update);
  if ("IntersectionObserver" in window) {
    new IntersectionObserver((entries) => {
      onScreen = entries[entries.length - 1].isIntersecting;
      update();
    }).observe(strip);
  }
  if ("ResizeObserver" in window) {
    let width = -1;
    new ResizeObserver(() => {
      if (strip.clientWidth !== width) {
        width = strip.clientWidth;
        evaluate();
      }
    }).observe(strip);
  } else {
    window.addEventListener("resize", evaluate);
    evaluate();
  }
}

window.addEventListener("DOMContentLoaded", () => {
  document.querySelectorAll(".hub-products-strip").forEach(hubProductCarousel);

  const a = getCoinCookie();
  if (a?.length === 3) {
    if (a[2] === "true") {
      changeCSSStyle(".prim-amt", "display", "none");
      changeCSSStyle(".sec-amt", "display", "initial");
    }
    document
      .querySelectorAll(".amt")
      .forEach(
        (e) => new bootstrap.Tooltip(e, { title: amountTooltip, html: true })
      );
  }

  document
    .querySelectorAll("[alias-type]")
    .forEach(
      (e) =>
        new bootstrap.Tooltip(e, { title: addressAliasTooltip, html: true })
    );

  document
    .querySelectorAll("[tt]")
    .forEach((e) => new bootstrap.Tooltip(e, { title: e.getAttribute("tt") }));

  document.querySelectorAll("#header .bb-group>.btn-check").forEach((e) =>
    e.addEventListener("click", (e) => {
      const a = getCoinCookie();
      const sc = e.target.id === "secondary-coin";
      if (a?.length === 3 && (a[2] === "true") !== sc) {
        document.cookie = `${a[0]}=${a[1]}=${sc}; Path=/`;
        changeCSSStyle(".prim-amt", "display", sc ? "none" : "initial");
        changeCSSStyle(".sec-amt", "display", sc ? "initial" : "none");
      }
    })
  );

  document.querySelectorAll(".copyable").forEach((e) =>
    e.addEventListener("click", (e) => {
      if (e.clientX < e.target.getBoundingClientRect().x) {
        let t = e.target.getAttribute("cc");
        if (!t) t = e.target.innerText;
        const textToCopy = t.trim();
        navigator.clipboard.writeText(textToCopy);
        e.target.className = e.target.className.replace("copyable", "copied");
        setTimeout(
          () =>
            (e.target.className = e.target.className.replace(
              "copied",
              "copyable"
            )),
          1000
        );
        e.preventDefault();
      }
    })
  );

  // hub copy buttons: <button type="button" class="hub-copy" data-copy="...">
  // The result is announced in a polite live region. Without the async clipboard API (non-secure
  // origin, old browser) or when it refuses, a hidden textarea and execCommand are tried; if that
  // also fails, the summary's ID text is selected so it can be copied by hand.
  const copyButtons = document.querySelectorAll("button[data-copy]");
  if (copyButtons.length) {
    const live = document.createElement("div");
    live.className = "visually-hidden";
    live.setAttribute("aria-live", "polite");
    document.body.appendChild(live);
    const announce = (msg) => {
      live.textContent = "";
      setTimeout(() => (live.textContent = msg), 50);
    };
    const legacyCopy = (text) => {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.top = "0";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      let ok = false;
      try {
        ok = document.execCommand("copy");
      } catch (e) {}
      ta.remove();
      return ok;
    };
    copyButtons.forEach((b) =>
      b.addEventListener("click", () => {
        const text = b.getAttribute("data-copy");
        const done = (ok) => {
          if (!ok) {
            const id = b.closest(".hub-summary")?.querySelector(".hub-summary-id") || b.previousElementSibling;
            if (id) {
              const range = document.createRange();
              range.selectNodeContents(id);
              const sel = window.getSelection();
              sel.removeAllRanges();
              sel.addRange(range);
            }
            announce("Copy failed. The text is selected, copy it manually.");
            return;
          }
          announce("Copied to clipboard");
          b.classList.add("hub-copied");
          if (b.classList.contains("hub-copy-text")) {
            b.dataset.label = b.dataset.label || b.textContent;
            b.textContent = "Copied";
          }
          clearTimeout(b.hubCopyTimer);
          b.hubCopyTimer = setTimeout(() => {
            b.classList.remove("hub-copied");
            if (b.dataset.label) b.textContent = b.dataset.label;
          }, 1000);
        };
        const p = navigator.clipboard && navigator.clipboard.writeText ? navigator.clipboard.writeText(text) : Promise.reject();
        p.then(
          () => done(true),
          () => done(legacyCopy(text))
        );
      })
    );
  }

  // keep the transaction in view when its input/output list is collapsed,
  // instead of leaving the viewport wherever the shrinking content left it
  document.querySelectorAll(".tx-in.collapse, .tx-out.collapse").forEach((e) =>
    e.addEventListener("hidden.bs.collapse", () =>
      e.closest(".tx-detail")?.scrollIntoView({ block: "nearest" })
    )
  );
});
