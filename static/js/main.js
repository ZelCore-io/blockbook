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
// scrolling row. Only while its real tiles do not fit (re-checked on resize), it becomes a slow
// carousel:
// - the tiles drift left at DRIFT px/s by advancing scrollLeft, so drift, swipe, wheel, drag,
//   keys and buttons all move one scroll position. One copy of the tiles follows the real ones
//   (aria-hidden, links out of the tab order), and scrollLeft jumps by exactly one set width
//   near the ends, which shows the same picture.
// - the drift pauses while the mouse is over the strip, keyboard focus is inside it, a pointer
//   or touch is down, the tab is hidden or the strip is off-screen, and resumes RESUME ms after
//   the last interaction. prefers-reduced-motion: reduce stops it and smooth scrolling.
// - a mouse can drag the row (a drag never opens a tile link); the ‹ › buttons and
//   ArrowLeft/ArrowRight move one tile; Home/End go to the first/last real tile.
// Scroll snapping is switched off in carousel mode (hub.css): it would fight the drift.
function hubProductCarousel(strip) {
  const DRIFT = 25;
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
  let clones = [];
  let setW = 0; // one tile set including its trailing gap
  let tileW = 0; // from one tile to the next
  let pos = 0; // sub-pixel scroll position while drifting
  let raf = 0;
  let last = 0;
  let resumeAt = 0;
  let resumeTimer = 0;
  let settleTimer = 0;
  let selfX = NaN; // scrollLeft this script set without animation
  let hover = false;
  let keyFocus = false;
  let mouseDown = false;
  let touchDown = false;
  let onScreen = true;

  const maxScroll = () => strip.scrollWidth - strip.clientWidth;
  // x and x - setW show the same picture; wrap() moves x into [0, setW)
  const wrap = (x) => {
    if (setW <= 0) return x;
    while (x >= setW) x -= setW;
    while (x < 0) x += setW;
    return x;
  };
  const jump = (x) => {
    if (Math.abs(strip.scrollLeft - x) >= 1) selfX = x;
    strip.scrollLeft = x;
  };
  const glide = (x) => {
    strip.scrollTo({ left: Math.min(x, maxScroll()), behavior: reduce.matches ? "auto" : "smooth" });
  };
  const measure = () => {
    setW = clones[0].getBoundingClientRect().left - real[0].getBoundingClientRect().left;
    tileW = setW / n;
  };
  const overflows = () => {
    const cs = getComputedStyle(strip);
    const box = strip.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    return real[n - 1].getBoundingClientRect().right - real[0].getBoundingClientRect().left > box + 1;
  };

  const evaluate = () => {
    const want = overflows();
    if (want === active) {
      if (active) {
        const at = strip.scrollLeft / (setW || 1);
        measure();
        jump(at * setW);
      }
      return;
    }
    active = want;
    if (active) {
      const frag = document.createDocumentFragment();
      clones = real.map((t) => {
        const c = t.cloneNode(true);
        c.classList.add("hub-product-clone");
        c.setAttribute("aria-hidden", "true");
        c.setAttribute("tabindex", "-1");
        c.querySelectorAll("a, button, [tabindex]").forEach((e) => e.setAttribute("tabindex", "-1"));
        frag.appendChild(c);
        return c;
      });
      strip.appendChild(frag);
      strip.classList.add("hub-products-carousel");
      measure();
      // start on the copy's first tile (same picture as 0) so a swipe back works at once
      jump(setW);
    } else {
      clones.forEach((c) => c.remove());
      clones = [];
      strip.classList.remove("hub-products-carousel", "hub-products-dragging");
      jump(0);
    }
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
    pos = wrap(pos + (DRIFT * dt) / 1000);
    strip.scrollLeft = pos;
    raf = requestAnimationFrame(frame);
  };
  function update() {
    clearTimeout(resumeTimer);
    if (canRun()) {
      if (!raf) {
        pos = wrap(strip.scrollLeft);
        last = 0;
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

  // one tile forward or back from the tile at the left edge
  function step(dir) {
    if (!active) return;
    let x = wrap(strip.scrollLeft);
    if (dir < 0 && x < tileW / 2) x += setW;
    jump(x);
    const i = dir > 0 ? Math.floor(x / tileW + 0.02) + 1 : Math.ceil(x / tileW - 0.02) - 1;
    glide(i * tileW);
  }
  // to real tile i (0..n-1); n is the copy of tile 0 and settles back onto it
  const goTo = (i, back) => {
    let x = wrap(strip.scrollLeft);
    if (back && i === n - 1 && x < tileW) x += setW;
    jump(x);
    glide(i * tileW);
  };

  // Near the ends of the scroll range jump one set width so scrolling can go on either way.
  // When scrolling stops past the copy's first tile (or on it, with keyboard focus inside, so
  // the focused real tile is the one in view), move back onto the real tiles.
  strip.addEventListener(
    "scroll",
    () => {
      if (!active || raf) return;
      const x = strip.scrollLeft;
      if (Math.abs(x - selfX) < 1) {
        selfX = NaN;
        return;
      }
      if (!mouseDown) {
        if (x <= 0 && !keyFocus) jump(setW);
        else if (x >= maxScroll() - 1) jump(x - setW);
      }
      clearTimeout(settleTimer);
      settleTimer = setTimeout(() => {
        selfX = NaN;
        const at = strip.scrollLeft;
        if (active && !raf && !mouseDown && !touchDown && (at > setW + 0.5 || (keyFocus && at > setW - 0.5))) jump(at - setW);
      }, 250);
    },
    { passive: true }
  );

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
  view.addEventListener("wheel", touched, { passive: true });
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
    let x = drag.left - dx;
    if (x <= 0) {
      drag.left += setW;
      x += setW;
    } else if (x >= maxScroll() - 1) {
      drag.left -= setW;
      x -= setW;
    }
    jump(x);
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

  strip.addEventListener("keydown", (e) => {
    if (!active || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const k = e.key;
    if (k !== "ArrowLeft" && k !== "ArrowRight" && k !== "Home" && k !== "End") return;
    e.preventDefault();
    keyFocus = true;
    touched();
    const tile = e.target.closest(".hub-product");
    const at = real.indexOf(tile);
    if (at < 0) {
      if (k === "Home" || k === "End") goTo(k === "Home" ? 0 : n - 1, false);
      else step(k === "ArrowRight" ? 1 : -1);
      return;
    }
    let i;
    if (k === "Home") i = 0;
    else if (k === "End") i = n - 1;
    else i = (at + (k === "ArrowRight" ? 1 : n - 1)) % n;
    real[i].focus({ preventScroll: true });
    if (k === "ArrowRight" && i === 0) glide(setW);
    else goTo(i, k === "ArrowLeft");
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
