// ==UserScript==
// @name            Zen Mac Tab Switcher
// @version         1.0.0
// @description     macOS Cmd+Tab-style Ctrl+Tab switcher for Zen: glass HUD, live tab previews, spring animations.
// @include         main
// ==/UserScript==

/*
 * Hold Ctrl and press Tab to cycle through tabs in most-recently-used order.
 * Release Ctrl to switch. A quick Ctrl+Tab tap flips to the previous tab
 * without showing the switcher, like Cmd+Tab on macOS.
 *
 * While the switcher is open (Ctrl held):
 *   Tab / Shift+Tab / `       move the selection (Ctrl+Shift+Tab from idle starts at the end)
 *   Arrow keys, Home, End     move the selection
 *   Mouse hover / click       select / switch immediately
 *   W or Q                    close the selected tab (pinned tabs follow Zen's close setting)
 *   Middle click              close the hovered tab
 *   Esc                       cancel
 *
 * Optional prefs (about:config), all under "zen.mac-tab-switcher.":
 *   enabled          bool    true    turn the switcher off without removing the file
 *   show-delay-ms    int     150     how long Ctrl+Tab must be held before the switcher appears
 *   max-tabs         int     24      most tabs shown
 *   transition       string  "zoom"  tab change animation: "zoom", "fade" or "none"
 *   glass            string  "auto"  frosted-glass blur: "auto" (off when Zen renders without a GPU), "on", "off"
 *   select-on-hover  bool    true    moving the mouse over a preview selects it
 *
 * Also follows Zen's own settings in Settings > Tabs ("Ignore Pending tabs when
 * cycling with Ctrl+Tab", "Ctrl+Tab cycles within Essential or Workspace tabs
 * only") and the system "reduce motion" setting.
 */

(function () {
  "use strict";

  if (window.location.href !== "chrome://browser/content/browser.xhtml") {
    return;
  }

  const ID = "zen-mac-tab-switcher";
  const PREF = "zen.mac-tab-switcher.";
  const HTML_NS = "http://www.w3.org/1999/xhtml";
  const DEFAULT_FAVICON = "chrome://global/skin/icons/defaultFavicon.svg";
  const THUMB_MAX_AGE_MS = 20000;
  const CAPTURE_CONCURRENCY = 3;

  const lazy = {};
  ChromeUtils.defineESModuleGetters(lazy, {
    PageThumbs: "resource://gre/modules/PageThumbs.sys.mjs",
    PrivateBrowsingUtils: "resource://gre/modules/PrivateBrowsingUtils.sys.mjs",
    ShortcutUtils: "resource://gre/modules/ShortcutUtils.sys.mjs",
  });
  let KeyboardLockUtils = null;
  try {
    ({ KeyboardLockUtils } = ChromeUtils.importESModule(
      "resource://gre/modules/KeyboardLockUtils.sys.mjs"
    ));
  } catch {
    // Older Gecko without keyboard-lock support.
  }

  /* ---------------------------------------------------------------- prefs */

  function readPref(type, name, fallback) {
    try {
      switch (type) {
        case "bool":
          return Services.prefs.getBoolPref(name, fallback);
        case "int":
          return Services.prefs.getIntPref(name, fallback);
        default:
          return Services.prefs.getStringPref(name, fallback);
      }
    } catch {
      return fallback; // pref exists with another type
    }
  }

  const Settings = {
    get enabled() {
      return (
        readPref("bool", PREF + "enabled", true) &&
        !readPref("bool", "browser.ctrlTab.disallowForScreenReaders", false)
      );
    },
    get showDelay() {
      return clamp(readPref("int", PREF + "show-delay-ms", 150), 0, 2000);
    },
    get maxTabs() {
      return clamp(readPref("int", PREF + "max-tabs", 24), 2, 200);
    },
    get transition() {
      const value = readPref("string", PREF + "transition", "zoom");
      return ["zoom", "fade", "none"].includes(value) ? value : "zoom";
    },
    get glass() {
      const value = readPref("string", PREF + "glass", "auto");
      return ["auto", "on", "off"].includes(value) ? value : "auto";
    },
    get hoverSelect() {
      return readPref("bool", PREF + "select-on-hover", true);
    },
    get ignorePending() {
      return readPref("bool", "zen.tabs.ctrl-tab.ignore-pending-tabs", false);
    },
    get sameKindOnly() {
      return readPref("bool", "zen.tabs.ctrl-tab.ignore-essential-tabs", false);
    },
  };

  /* ---------------------------------------------------------------- utils */

  function clamp(value, min, max) {
    return Math.min(max, Math.max(min, value));
  }

  function el(doc, tag, attrs = {}) {
    const node = doc.createElementNS(HTML_NS, tag);
    for (const [key, value] of Object.entries(attrs)) {
      node.setAttribute(key, value);
    }
    return node;
  }

  function swallow(event) {
    event.preventDefault();
    event.stopImmediatePropagation();
  }

  /**
   * A damped spring sampled into a CSS linear() easing, so CSS transitions and
   * Web Animations get the same physically based motion macOS uses.
   *
   * @param {number} response  seconds for one undamped oscillation (lower = snappier)
   * @param {number} damping   damping ratio (1 = no overshoot)
   */
  function spring(response, damping) {
    const zeta = clamp(damping, 0.05, 0.99);
    const w0 = (2 * Math.PI) / response;
    const wd = w0 * Math.sqrt(1 - zeta * zeta);
    const seconds = Math.log(1000) / (zeta * w0); // settles within 0.1%
    const samples = 60;
    const points = [];
    for (let i = 0; i <= samples; i++) {
      const t = (i / samples) * seconds;
      const envelope = Math.exp(-zeta * w0 * t);
      const x =
        1 -
        envelope * (Math.cos(wd * t) + ((zeta * w0) / wd) * Math.sin(wd * t));
      points.push(Number(x.toFixed(4)));
    }
    points[samples] = 1;
    return {
      easing: `linear(${points.join(", ")})`,
      duration: Math.round(seconds * 1000),
    };
  }

  const SPRING_PANEL = spring(0.4, 0.78);
  const SPRING_ITEM = spring(0.48, 0.8);
  const SPRING_MOVE = spring(0.3, 0.84);
  const SPRING_ZOOM = spring(0.4, 0.9);

  function isCycleTabs(event) {
    try {
      const { ShortcutUtils } = lazy;
      return (
        ShortcutUtils.getSystemActionForEvent(event) === ShortcutUtils.CYCLE_TABS
      );
    } catch {
      return (
        event.keyCode === event.DOM_VK_TAB &&
        event.ctrlKey &&
        !event.altKey &&
        !(event.metaKey && AppConstants.platform != "win")
      );
    }
  }

  function prefersReducedMotion(win) {
    return win.matchMedia("(prefers-reduced-motion: reduce)").matches;
  }

  /**
   * Live blur behind a moving UI is cheap on the GPU but costs hundreds of
   * milliseconds per frame when Zen falls back to software rendering.
   */
  function useGlass(win) {
    switch (Settings.glass) {
      case "on":
        return true;
      case "off":
        return false;
    }
    if (win.matchMedia("(prefers-reduced-transparency: reduce)").matches) {
      return false; // like macOS with "Reduce transparency" turned on
    }
    try {
      return !/software|basic/i.test(win.windowUtils.layerManagerType);
    } catch {
      return true;
    }
  }

  /** Mirrors how Zen picks the color scheme of its own floating UI. */
  function isDarkUI(win) {
    if (lazy.PrivateBrowsingUtils.isWindowPrivate(win)) {
      return true;
    }
    const attr = win.document.documentElement.getAttribute(
      "zen-should-be-dark-mode"
    );
    if (attr === "true" || attr === "false") {
      return attr === "true";
    }
    switch (readPref("int", "zen.view.window.scheme", 2)) {
      case 0:
        return true;
      case 1:
        return false;
    }
    const system = win.matchMedia("(-moz-system-dark-theme)");
    if (system.media !== "not all") {
      return system.matches;
    }
    return win.matchMedia("(prefers-color-scheme: dark)").matches;
  }

  function tabURL(tab) {
    try {
      return tab.linkedBrowser?.currentURI?.spec || "";
    } catch {
      return "";
    }
  }

  function tabHost(tab) {
    try {
      const uri = tab.linkedBrowser?.currentURI;
      if (!uri) {
        return "";
      }
      if (uri.schemeIs("about") || uri.schemeIs("chrome")) {
        return uri.spec.split(/[?#]/)[0];
      }
      if (uri.schemeIs("file")) {
        return decodeURIComponent(uri.spec.split("/").pop() || "file");
      }
      return (uri.displayHost || uri.host || uri.scheme).replace(/^www\./, "");
    } catch {
      return "";
    }
  }

  /* ------------------------------------------------------------------ CSS */

  const CSS = `
#${ID} {
  --mts-card-w: 220px;
  --mts-card-h: 138px;
  --mts-item-w: 240px;
  --mts-viewport-w: 240px;
  --mts-pad: 10px;
  --mts-gap: 6px;
  --mts-badge: 30px;
  --mts-below: 64px;
  --mts-sq: var(--zen-squircle-value, 1);

  /* Override the popover UA styles: a full-window, transparent layer. */
  position: fixed;
  inset: 0;
  width: auto;
  height: auto;
  max-width: none;
  max-height: none;
  margin: 0;
  padding: 0;
  border: 0;
  overflow: visible;
  background: transparent;
  color: inherit;
  z-index: 2147483000;

  display: none;
  align-items: center;
  justify-content: center;
  pointer-events: none;
  color-scheme: light;
  font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "SF Pro",
    "Segoe UI Variable Text", "Segoe UI", Inter, Cantarell, system-ui, sans-serif;
  -moz-osx-font-smoothing: grayscale;
  user-select: none;
  cursor: default;

  &[dark] { color-scheme: dark; }
  &[open] { pointer-events: auto; }

  & .mts-panel {
    position: relative;
    padding: 12px;
    border-radius: calc(26px * var(--mts-sq));
    background:
      linear-gradient(to bottom,
        light-dark(rgb(255 255 255 / 0.32), rgb(255 255 255 / 0.05)),
        transparent 45%),
      light-dark(rgb(244 244 247 / 0.62), rgb(28 28 31 / 0.72));
    backdrop-filter: blur(40px) saturate(1.9);
    box-shadow:
      0 0 0 0.5px light-dark(rgb(0 0 0 / 0.14), rgb(0 0 0 / 0.8)),
      0 30px 80px -18px rgb(0 0 0 / 0.55),
      0 10px 28px -12px rgb(0 0 0 / 0.35);
    will-change: transform, opacity;
  }

  /* Liquid-glass rim: a bright specular edge that fades around the panel. */
  & .mts-panel::before {
    content: "";
    position: absolute;
    inset: 0;
    padding: 1px;
    border-radius: inherit;
    corner-shape: inherit;
    background: linear-gradient(165deg,
      light-dark(rgb(255 255 255 / 0.95), rgb(255 255 255 / 0.38)) 0%,
      light-dark(rgb(255 255 255 / 0.3), rgb(255 255 255 / 0.08)) 30%,
      transparent 55%,
      light-dark(rgb(255 255 255 / 0.45), rgb(255 255 255 / 0.14)) 100%);
    mask: linear-gradient(#000 0 0) content-box exclude, linear-gradient(#000 0 0);
    pointer-events: none;
  }

  & .mts-viewport {
    position: relative;
    width: var(--mts-viewport-w);
    border-radius: calc(17px * var(--mts-sq));
  }

  &[overflowing] .mts-viewport {
    overflow: clip;
    mask-image: linear-gradient(to right,
      var(--mts-fade-start, #000) 0, #000 48px,
      #000 calc(100% - 48px), var(--mts-fade-end, #000) 100%);
  }
  &[fade-start] { --mts-fade-start: transparent; }
  &[fade-end] { --mts-fade-end: transparent; }

  & .mts-track {
    position: relative;
    display: flex;
    gap: var(--mts-gap);
    width: max-content;
    direction: ltr;
    transition: transform var(--mts-move-dur) var(--mts-move-ease);
  }

  & .mts-highlight {
    position: absolute;
    top: 0;
    bottom: 0;
    left: 0;
    width: var(--mts-item-w);
    border-radius: calc(17px * var(--mts-sq));
    background: light-dark(rgb(0 0 0 / 0.075), rgb(255 255 255 / 0.14));
    box-shadow: inset 0 0 0 0.5px light-dark(rgb(0 0 0 / 0.06), rgb(255 255 255 / 0.12));
    transition: transform var(--mts-move-dur) var(--mts-move-ease);
    will-change: transform;
  }

  & .mts-caption {
    position: absolute;
    left: 8px;
    right: 8px;
    bottom: 9px;
    text-align: center;
    line-height: 1.25;
  }

  & .mts-title,
  & .mts-host {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  & .mts-title {
    font-size: 12.5px;
    font-weight: 600;
    color: light-dark(rgb(0 0 0 / 0.88), rgb(255 255 255 / 0.95));
  }

  & .mts-host {
    margin-top: 1px;
    font-size: 11px;
    font-weight: 500;
    color: light-dark(rgb(0 0 0 / 0.5), rgb(255 255 255 / 0.55));
  }

  & .mts-item {
    position: relative;
    flex: none;
    box-sizing: border-box;
    width: var(--mts-item-w);
    padding: var(--mts-pad) var(--mts-pad) var(--mts-below);
  }

  & .mts-thumb {
    position: relative;
    width: var(--mts-card-w);
    height: var(--mts-card-h);
    overflow: hidden;
    border-radius: calc(11px * var(--mts-sq));
    background: light-dark(rgb(255 255 255), rgb(40 40 43));
    box-shadow:
      0 0 0 0.5px light-dark(rgb(0 0 0 / 0.16), rgb(255 255 255 / 0.12)),
      0 8px 22px -10px rgb(0 0 0 / 0.6);
  }

  & .mts-shot {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: cover;
    object-position: top;
  }

  & .mts-ph {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    background: light-dark(rgb(234 234 239), rgb(46 46 50));
  }

  /* Blurred favicon backdrop for tabs without a preview; pre-rendered once. */
  & .mts-ph-glow {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    opacity: 0.6;
  }

  & .mts-thumb[has-shot] .mts-ph {
    visibility: hidden;
  }

  & .mts-ph-icon {
    position: relative;
    width: 40px;
    height: 40px;
    filter: drop-shadow(0 6px 14px rgb(0 0 0 / 0.3));
  }

  & .mts-badge {
    position: absolute;
    left: calc(50% - var(--mts-badge) / 2);
    top: calc(var(--mts-pad) + var(--mts-card-h) - var(--mts-badge) / 2);
    box-sizing: border-box;
    width: var(--mts-badge);
    height: var(--mts-badge);
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: calc(8px * var(--mts-sq));
    background: light-dark(rgb(255 255 255 / 0.97), rgb(60 60 64 / 0.97));
    box-shadow:
      0 0 0 0.5px light-dark(rgb(0 0 0 / 0.14), rgb(255 255 255 / 0.1)),
      0 3px 10px -2px rgb(0 0 0 / 0.4);
  }

  & .mts-favicon {
    width: 18px;
    height: 18px;
  }

  & :is(.mts-favicon, .mts-ph-icon) {
    -moz-context-properties: fill, fill-opacity, stroke, stroke-opacity;
    fill: currentColor;
    stroke: currentColor;
    color: light-dark(rgb(29 29 31), rgb(245 245 247));
  }

  /* No GPU / reduced transparency: same look, without the live blur behind the panel. */
  &[lite] .mts-panel {
    backdrop-filter: none;
    background:
      linear-gradient(to bottom,
        light-dark(rgb(255 255 255 / 0.5), rgb(255 255 255 / 0.06)),
        transparent 45%),
      light-dark(rgb(238 238 242 / 0.96), rgb(34 34 37 / 0.96));
  }
  &[lite] .mts-ghost {
    box-shadow: none;
  }

  /* The picked preview flying into place (Mission Control-style zoom). */
  & .mts-ghost {
    position: fixed;
    overflow: hidden;
    transform-origin: 0 0;
    corner-shape: round;
    background: light-dark(rgb(255 255 255), rgb(32 32 32));
    box-shadow: 0 24px 60px -16px rgb(0 0 0 / 0.5);
    pointer-events: none;
    will-change: transform, opacity;
  }
}

#${ID}:popover-open { display: flex; }
#${ID}[fallback-open] { display: flex; }

@media (prefers-reduced-motion: reduce) {
  #${ID},
  #${ID} * {
    transition: none !important;
  }
}
`;

  /* -------------------------------------------------------------- switcher */

  class MacTabSwitcher {
    constructor(win) {
      this.win = win;
      this.doc = win.document;
      this.state = "idle"; // "idle" | "pending" (Ctrl+Tab held, HUD not shown yet) | "open"
      this.tabs = [];
      this.items = [];
      this.index = 0;
      this.session = 0;
      this.metrics = null;
      this.scrollX = 0;
      this.pointer = null;
      this.showTimer = null;
      this.hiResTimer = null;
      this.hiRes = null; // sharper capture of the selected tab, used for the zoom
      this.hideAnimation = null;
      this.hideToken = 0;
      this.thumbs = new Map(); // tab -> { node, url, time, width }
    }

    get gBrowser() {
      return this.win.gBrowser;
    }

    get active() {
      return this.state !== "idle";
    }

    init() {
      const doc = this.doc;
      this.style = el(doc, "style", { id: `${ID}-style` });
      this.style.textContent = CSS;
      (doc.head || doc.documentElement).append(this.style);

      this.root = el(doc, "div", { id: ID });
      // A manual popover lives in the top layer, above all of Zen's UI and
      // above fullscreen video.
      this.usePopover = typeof this.root.showPopover === "function";
      if (this.usePopover) {
        this.root.setAttribute("popover", "manual");
      }
      this.panel = el(doc, "div", { class: "mts-panel" });
      this.viewport = el(doc, "div", { class: "mts-viewport" });
      this.track = el(doc, "div", { class: "mts-track" });
      this.highlight = el(doc, "div", { class: "mts-highlight" });
      this.caption = el(doc, "div", { class: "mts-caption" });
      this.titleEl = el(doc, "div", { class: "mts-title" });
      this.hostEl = el(doc, "div", { class: "mts-host" });
      this.caption.append(this.titleEl, this.hostEl);
      this.highlight.append(this.caption);
      this.track.append(this.highlight);
      this.viewport.append(this.track);
      this.panel.append(this.viewport);
      this.root.append(this.panel);
      this.root.style.setProperty("--mts-move-ease", SPRING_MOVE.easing);
      this.root.style.setProperty("--mts-move-dur", `${SPRING_MOVE.duration}ms`);
      doc.body.append(this.root);

      for (const type of ["mousemove", "mousedown", "mouseup", "click", "auxclick", "contextmenu"]) {
        this.root.addEventListener(type, this);
      }
      // System-group capture on the window runs before Firefox's own Ctrl+Tab
      // handlers (tabbox and ctrlTab both listen on the document's system group).
      for (const type of ["keydown", "keyup", "keypress"]) {
        this.win.addEventListener(type, this, { capture: true, mozSystemGroup: true });
      }
      this.win.addEventListener("deactivate", this);
      this.win.addEventListener("resize", this);
      this.win.addEventListener("unload", this, { once: true });
      this.gBrowser.tabContainer.addEventListener("TabClose", this);
      this.gBrowser.tabContainer.addEventListener("TabAttrModified", this);
    }

    destroy() {
      this.finish(false);
      this.cancelTransition();
      for (const type of ["keydown", "keyup", "keypress"]) {
        this.win.removeEventListener(type, this, { capture: true, mozSystemGroup: true });
      }
      this.win.removeEventListener("deactivate", this);
      this.win.removeEventListener("resize", this);
      this.win.removeEventListener("unload", this);
      this.gBrowser?.tabContainer?.removeEventListener("TabClose", this);
      this.gBrowser?.tabContainer?.removeEventListener("TabAttrModified", this);
      this.root?.remove();
      this.style?.remove();
      this.thumbs.clear();
      if (this.win.gZenMacTabSwitcher === this) {
        delete this.win.gZenMacTabSwitcher;
      }
    }

    handleEvent(event) {
      switch (event.type) {
        case "keydown":
          this.onKeyDown(event);
          break;
        case "keyup":
          this.onKeyUp(event);
          break;
        case "keypress":
          if (this.active && event.isTrusted) {
            swallow(event);
          }
          break;
        case "deactivate":
          this.finish(false);
          break;
        case "resize":
          if (this.state === "open") {
            this.layout();
            this.updateSelection(false);
          }
          break;
        case "TabClose":
          this.thumbs.delete(event.target);
          this.removeFromSwitcher(event.target);
          break;
        case "TabAttrModified":
          this.onTabAttrModified(event);
          break;
        case "mousemove":
          this.onMouseMove(event);
          break;
        case "mouseup":
          this.onMouseUp(event);
          break;
        case "mousedown":
        case "click":
        case "auxclick":
        case "contextmenu":
          event.preventDefault();
          event.stopPropagation();
          break;
        case "unload":
          this.destroy();
          break;
      }
    }

    /* ----------------------------------------------------------- keyboard */

    onKeyDown(event) {
      if (!event.isTrusted) {
        return;
      }
      if (this.active) {
        this.onKeyDownWhileActive(event);
        return;
      }
      if (!isCycleTabs(event) || !Settings.enabled) {
        return;
      }
      // Same guards as Firefox's own handler: a page may legitimately keep
      // Ctrl+Tab while it holds the keyboard lock in fullscreen.
      if (
        event.defaultPrevented ||
        KeyboardLockUtils?.mustWaitForKeyboardLockRequestedReply(event)
      ) {
        return;
      }
      const tabs = this.collectTabs();
      if (tabs.length < 2) {
        return;
      }
      swallow(event);
      this.start(tabs, event.shiftKey ? -1 : 1);
    }

    onKeyDownWhileActive(event) {
      if (!event.ctrlKey && event.key !== "Control") {
        // Ctrl was released without us seeing the keyup (e.g. focus moved).
        this.finish(true);
        return;
      }
      swallow(event);
      if (event.code === "Backquote") {
        this.move(-1); // Cmd+` goes backwards in the macOS switcher
        return;
      }
      switch (event.key) {
        case "Tab":
          this.move(event.shiftKey ? -1 : 1);
          break;
        case "ArrowRight":
        case "ArrowDown":
          this.move(1);
          break;
        case "ArrowLeft":
        case "ArrowUp":
          this.move(-1);
          break;
        case "Home":
          this.moveTo(0);
          break;
        case "End":
          this.moveTo(this.tabs.length - 1);
          break;
        case "Escape":
          this.finish(false);
          break;
        case "Enter":
          this.finish(true);
          break;
        case "Delete":
          this.closeSelected();
          break;
        default:
          if (["w", "q"].includes(event.key.toLowerCase())) {
            this.closeSelected();
          }
      }
    }

    onKeyUp(event) {
      if (!this.active || !event.isTrusted) {
        return;
      }
      // Like Firefox's switcher: keep keyups away from the page while cycling.
      swallow(event);
      if (event.key === "Control" || !event.ctrlKey) {
        this.finish(true);
      }
    }

    /* ------------------------------------------------------------ session */

    collectTabs() {
      const gBrowser = this.gBrowser;
      const selected = gBrowser.selectedTab;
      const ignorePending = Settings.ignorePending;
      const sameKindOnly = Settings.sameKindOnly;
      const selectedIsEssential = selected.hasAttribute("zen-essential");
      // gBrowser.tabs is already scoped to the current Zen space (essentials,
      // pinned and regular tabs, including tabs inside collapsed folders).
      const tabs = Array.prototype.filter.call(
        gBrowser.tabs,
        tab =>
          tab !== selected &&
          tab.isConnected &&
          !tab.closing &&
          !tab.hidden &&
          !tab.hasAttribute("zen-empty-tab") &&
          !tab.hasAttribute("zen-glance-tab") &&
          !(ignorePending && tab.hasAttribute("pending")) &&
          !(sameKindOnly && tab.hasAttribute("zen-essential") !== selectedIsEssential)
      );
      tabs.sort((a, b) => b.lastAccessed - a.lastAccessed);
      tabs.unshift(selected);
      return tabs.slice(0, Settings.maxTabs);
    }

    start(tabs, direction) {
      this.session++;
      this.tabs = tabs;
      this.index = direction > 0 ? 1 : tabs.length - 1;
      this.state = "pending";
      this.warmup();
      const delay = Settings.showDelay;
      if (delay > 0) {
        this.showTimer = this.win.setTimeout(() => this.show(), delay);
        // Use the hold delay to capture previews, so the HUD appears complete
        // instead of filling in while it animates.
        const session = this.session;
        this.prefetchTimer = this.win.setTimeout(
          () => this.prefetch(session),
          Math.min(40, delay / 3)
        );
      } else {
        this.show();
      }
    }

    move(delta) {
      const count = this.tabs.length;
      if (count) {
        this.moveTo((this.index + delta + count) % count);
      }
    }

    moveTo(index) {
      this.index = clamp(index, 0, this.tabs.length - 1);
      this.warmup();
      if (this.state === "pending") {
        this.show();
      } else {
        this.updateSelection(true);
      }
      this.scheduleHiRes();
    }

    /** Pre-render the highlighted tab so the switch is instant, like Firefox's own switcher. */
    warmup() {
      const tab = this.tabs[this.index];
      if (tab && !tab.selected) {
        try {
          this.gBrowser.warmupTab(tab);
        } catch {}
      }
    }

    prefetch(session) {
      if (this.prefetching === session || session !== this.session) {
        return;
      }
      this.prefetching = session;
      this.loadThumbnails(session).then(() =>
        this.captureHiRes(session, this.tabs[this.index])
      );
    }

    show() {
      this.win.clearTimeout(this.showTimer);
      this.showTimer = null;
      if (this.state !== "pending") {
        return;
      }
      this.state = "open";
      this.win.clearTimeout(this.prefetchTimer);
      this.cancelHide();
      this.cancelTransition(); // a zoom from the previous switch may still be running
      this.root.toggleAttribute("dark", isDarkUI(this.win));
      this.root.toggleAttribute("lite", !useGlass(this.win));
      this.renderItems();
      this.setVisible(true);
      this.root.setAttribute("open", "");
      this.scrollX = 0;
      this.pointer = null;
      this.layout();
      this.updateSelection(false);
      this.animateIn();
      this.prefetch(this.session); // no-op when the hold delay already started it
    }

    finish(commit) {
      if (!this.active) {
        return;
      }
      this.win.clearTimeout(this.showTimer);
      this.win.clearTimeout(this.hiResTimer);
      this.win.clearTimeout(this.prefetchTimer);
      this.showTimer = null;
      const wasOpen = this.state === "open";
      const target = commit ? this.tabs[this.index] : null;
      const item = wasOpen ? this.items[this.index] : null;
      this.state = "idle";
      this.session++;
      this.tabs = [];
      const switching =
        target &&
        target !== this.gBrowser.selectedTab &&
        target.isConnected &&
        !target.closing;
      const transition = switching ? this.playTransition(target, item) : [];
      if (wasOpen) {
        this.animateOut(transition);
      }
      if (switching) {
        this.selectTab(target);
      }
      this.hiRes = null;
    }

    selectTab(tab) {
      const gBrowser = this.gBrowser;
      try {
        const metrics = gBrowser.TabMetrics;
        if (metrics?.userTriggeredContext) {
          gBrowser.setSelectedTab(
            tab,
            metrics.userTriggeredContext(metrics.METRIC_SOURCE?.CTRL_TAB)
          );
          return;
        }
      } catch (error) {
        console.error(error);
      }
      gBrowser.selectedTab = tab;
    }

    closeSelected() {
      const tab = this.tabs[this.index];
      if (this.state === "open" && tab) {
        this.closeTab(tab);
      }
    }

    closeTab(tab) {
      const pinnedManager = this.win.gZenPinnedTabManager;
      if (tab.pinned && pinnedManager?.onCloseTabShortcut) {
        // Same as the close shortcut on a pinned tab: follows Zen's setting
        // (unload, reset, close...). Pinned tabs often stay open but unloaded,
        // so drop them from the switcher like a quit app leaves Cmd+Tab.
        const fakeEvent = { preventDefault() {}, stopPropagation() {} };
        Promise.resolve(pinnedManager.onCloseTabShortcut(fakeEvent, tab)).catch(
          console.error
        );
        this.removeFromSwitcher(tab);
        return;
      }
      this.gBrowser.removeTab(tab, { animate: true });
    }

    removeFromSwitcher(tab) {
      const index = this.tabs.indexOf(tab);
      if (!this.active || index < 0) {
        return;
      }
      this.tabs.splice(index, 1);
      if (this.tabs.length < 2) {
        this.finish(false);
        return;
      }
      if (index < this.index || this.index >= this.tabs.length) {
        this.index = Math.max(0, this.index - 1);
      }
      if (this.state === "open") {
        const [item] = this.items.splice(index, 1);
        item?.remove();
        this.layout();
        this.updateSelection(true);
      }
      this.warmup();
      this.scheduleHiRes();
    }

    onTabAttrModified(event) {
      if (this.state !== "open") {
        return;
      }
      const changed = event.detail?.changed || [];
      if (!changed.includes("label") && !changed.includes("image")) {
        return;
      }
      const index = this.tabs.indexOf(event.target);
      const item = this.items[index];
      if (!item) {
        return;
      }
      const icon = event.target.image || DEFAULT_FAVICON;
      for (const img of item.querySelectorAll(".mts-favicon, .mts-ph-icon")) {
        if (img.getAttribute("src") !== icon) {
          img.setAttribute("src", icon);
        }
      }
      const glow = item.querySelector(".mts-ph-glow");
      if (glow && glow._src !== icon) {
        this.paintGlow(glow, icon);
      }
      if (index === this.index) {
        this.updateCaption(false);
      }
    }

    /* -------------------------------------------------------------- mouse */

    itemFromEvent(event) {
      const item = event.target.closest?.(".mts-item");
      return item && this.items.includes(item) ? item : null;
    }

    onMouseMove(event) {
      if (this.state !== "open" || !Settings.hoverSelect) {
        return;
      }
      // Ignore the synthetic mousemove Gecko fires when the HUD appears under
      // a resting pointer; only real movement should change the selection.
      const last = this.pointer;
      this.pointer = { x: event.screenX, y: event.screenY };
      if (!last || Math.hypot(last.x - event.screenX, last.y - event.screenY) < 3) {
        return;
      }
      const item = this.itemFromEvent(event);
      const index = item ? this.items.indexOf(item) : -1;
      if (index >= 0 && index !== this.index) {
        this.moveTo(index);
      }
    }

    onMouseUp(event) {
      if (this.state !== "open") {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      const item = this.itemFromEvent(event);
      if (!item) {
        if (!this.panel.contains(event.target)) {
          this.finish(false); // click outside the HUD dismisses it
        }
        return;
      }
      if (event.button === 1) {
        this.closeTab(item._tab);
        return;
      }
      // Primary click, or Ctrl+click on macOS which arrives as button 2.
      this.index = this.items.indexOf(item);
      this.finish(true);
    }

    /* ---------------------------------------------------------- rendering */

    setVisible(visible) {
      if (this.usePopover) {
        try {
          const isOpen = this.root.matches(":popover-open");
          if (visible && !isOpen) {
            this.root.showPopover();
          } else if (!visible && isOpen) {
            this.root.hidePopover();
          }
          return;
        } catch (error) {
          console.error(error);
          this.usePopover = false;
          this.root.removeAttribute("popover");
        }
      }
      this.root.toggleAttribute("fallback-open", visible);
    }

    renderItems() {
      this.clearItems();
      const fragment = this.doc.createDocumentFragment();
      this.items = this.tabs.map(tab => {
        const item = this.createItem(tab);
        fragment.append(item);
        return item;
      });
      this.track.append(fragment);
    }

    clearItems() {
      for (const item of this.items) {
        item.remove();
      }
      this.items = [];
    }

    createItem(tab) {
      const doc = this.doc;
      const icon = tab.image || DEFAULT_FAVICON;
      const item = el(doc, "div", { class: "mts-item" });
      const thumb = el(doc, "div", { class: "mts-thumb" });
      const placeholder = el(doc, "div", { class: "mts-ph" });
      const glow = el(doc, "canvas", { class: "mts-ph-glow" });
      glow.width = 48;
      glow.height = 30;
      this.paintGlow(glow, icon);
      placeholder.append(glow, this.createIcon(icon, "mts-ph-icon"));
      thumb.append(placeholder);
      const cached = this.thumbs.get(tab);
      if (cached && cached.url === tabURL(tab)) {
        thumb.append(cached.node);
        thumb.setAttribute("has-shot", "");
      }
      const badge = el(doc, "div", { class: "mts-badge" });
      badge.append(this.createIcon(icon, "mts-favicon"));
      item.append(thumb, badge);
      item._tab = tab;
      item._thumb = thumb;
      return item;
    }

    /** Blur the favicon once into a tiny canvas; scaling it up costs nothing per frame. */
    paintGlow(canvas, src) {
      canvas._src = src;
      const img = new this.win.Image();
      img.addEventListener(
        "load",
        () => {
          if (canvas._src !== src) {
            return;
          }
          try {
            const ctx = canvas.getContext("2d");
            ctx.clearRect(0, 0, canvas.width, canvas.height);
            ctx.filter = "blur(5px) saturate(1.8)";
            ctx.drawImage(img, -10, -14, canvas.width + 20, canvas.height + 28);
          } catch {}
        },
        { once: true }
      );
      img.src = src;
    }

    createIcon(src, className) {
      const img = el(this.doc, "img", { class: className, alt: "" });
      img.addEventListener(
        "error",
        () => img.setAttribute("src", DEFAULT_FAVICON),
        { once: true }
      );
      img.setAttribute("src", src);
      return img;
    }

    /** The rounded box Zen draws the page in (the whole area when split view is showing). */
    contentBox() {
      const panels = this.gBrowser.tabpanels;
      const splitShowing = panels?.getAttribute("zen-split-view") === "true";
      const container =
        (!splitShowing &&
          this.gBrowser.selectedBrowser?.closest(".browserSidebarContainer")) ||
        panels;
      const rect = container?.getBoundingClientRect();
      return rect?.width > 0 && rect?.height > 0 ? { container, rect } : null;
    }

    layout() {
      const win = this.win;
      const count = Math.max(1, this.items.length);
      const PAD = 10;
      const GAP = 6;
      const PANEL_PAD = 12;
      const BELOW = 64; // badge overhang + caption
      const box = this.contentBox();
      const aspect = clamp(box ? box.rect.width / box.rect.height : 1.6, 1.25, 1.9);
      const maxRowWidth = Math.min(win.innerWidth - 120, 1480) - PANEL_PAD * 2;
      const maxCardW = clamp(Math.round(win.innerWidth * 0.12), 140, 212);
      let cardW = Math.floor((maxRowWidth - GAP * (count - 1)) / count) - PAD * 2;
      cardW = clamp(cardW, 132, maxCardW);
      let cardH = Math.round(cardW / aspect);
      const maxCardH = Math.max(
        64,
        Math.floor(win.innerHeight * 0.5) - BELOW - PAD - PANEL_PAD * 2
      );
      if (cardH > maxCardH) {
        cardH = maxCardH;
        cardW = Math.round(cardH * aspect);
      }
      const itemW = cardW + PAD * 2;
      const trackW = count * itemW + (count - 1) * GAP;
      const viewportW = Math.min(trackW, Math.max(itemW, maxRowWidth));
      this.metrics = {
        cardW,
        cardH,
        itemW,
        gap: GAP,
        trackW,
        viewportW,
        overflow: trackW > viewportW + 0.5,
      };
      const style = this.root.style;
      style.setProperty("--mts-card-w", `${cardW}px`);
      style.setProperty("--mts-card-h", `${cardH}px`);
      style.setProperty("--mts-item-w", `${itemW}px`);
      style.setProperty("--mts-viewport-w", `${viewportW}px`);
      style.setProperty("--mts-pad", `${PAD}px`);
      style.setProperty("--mts-gap", `${GAP}px`);
      style.setProperty("--mts-below", `${BELOW}px`);
      this.root.toggleAttribute("overflowing", this.metrics.overflow);
    }

    updateSelection(animate) {
      const m = this.metrics;
      if (!m) {
        return;
      }
      const x = this.index * (m.itemW + m.gap);
      if (m.overflow) {
        const margin = Math.min(m.itemW * 0.75, (m.viewportW - m.itemW) / 2);
        let scrollX = this.scrollX;
        if (x - margin < scrollX) {
          scrollX = x - margin;
        } else if (x + m.itemW + margin > scrollX + m.viewportW) {
          scrollX = x + m.itemW + margin - m.viewportW;
        }
        this.scrollX = clamp(scrollX, 0, m.trackW - m.viewportW);
      } else {
        this.scrollX = 0;
      }
      if (!animate) {
        this.track.style.transition = "none";
        this.highlight.style.transition = "none";
      }
      this.track.style.transform = `translate3d(${-this.scrollX}px, 0, 0)`;
      this.highlight.style.transform = `translate3d(${x}px, 0, 0)`;
      this.root.toggleAttribute("fade-start", m.overflow && this.scrollX > 1);
      this.root.toggleAttribute(
        "fade-end",
        m.overflow && this.scrollX < m.trackW - m.viewportW - 1
      );
      if (!animate) {
        this.highlight.getBoundingClientRect(); // commit the jump before re-enabling transitions
        this.track.style.transition = "";
        this.highlight.style.transition = "";
      }
      this.items.forEach((item, i) => item.toggleAttribute("selected", i === this.index));
      this.updateCaption(animate);
    }

    updateCaption(animate) {
      const tab = this.tabs[this.index];
      if (!tab) {
        return;
      }
      this.titleEl.textContent = tab.label || "";
      this.hostEl.textContent = tabHost(tab);
      if (animate && !prefersReducedMotion(this.win)) {
        this.caption.animate([{ opacity: 0.2 }, { opacity: 1 }], {
          duration: 180,
          easing: "ease-out",
        });
      }
    }

    /* --------------------------------------------------------- animation */

    animateIn() {
      if (prefersReducedMotion(this.win)) {
        return;
      }
      this.panel.animate([{ opacity: 0 }, { opacity: 1 }], {
        duration: 130,
        easing: "ease-out",
      });
      this.panel.animate(
        [{ transform: "scale(0.94)" }, { transform: "scale(1)" }],
        { duration: SPRING_PANEL.duration, easing: SPRING_PANEL.easing }
      );
      const delayOf = i => 20 + Math.min(i, 13) * 14;
      this.items.slice(0, 14).forEach((item, i) => {
        item.animate(
          [
            { opacity: 0, transform: "translateY(8px) scale(0.95)" },
            { opacity: 1, transform: "none" },
          ],
          {
            duration: SPRING_ITEM.duration,
            easing: SPRING_ITEM.easing,
            delay: delayOf(i),
            fill: "backwards",
          }
        );
      });
      // The highlight and caption appear together with their card.
      this.highlight.animate([{ opacity: 0 }, { opacity: 1 }], {
        duration: 220,
        easing: "ease-out",
        delay: delayOf(this.index),
        fill: "backwards",
      });
    }

    /** @param {Animation[]} others  animations (e.g. the zoom) to wait for before hiding */
    animateOut(others = []) {
      this.root.removeAttribute("open");
      const token = ++this.hideToken;
      const done = () => {
        if (token !== this.hideToken || this.state === "open") {
          return; // a newer session owns the HUD now
        }
        this.cancelHide();
        this.setVisible(false);
        this.clearItems();
        this.clearGhosts();
      };
      if (prefersReducedMotion(this.win)) {
        done();
        return;
      }
      // Safety net: never leave the HUD on screen if the compositor stalls.
      this.win.setTimeout(done, 1200);
      this.hideAnimation = this.panel.animate(
        [
          { opacity: 1, transform: "scale(1)" },
          { opacity: 0, transform: "scale(0.96)" },
        ],
        { duration: 170, easing: "cubic-bezier(0.4, 0, 0.6, 1)", fill: "forwards" }
      );
      Promise.allSettled(
        [this.hideAnimation, ...others].map(animation => animation.finished)
      ).then(done);
    }

    cancelHide() {
      this.hideAnimation?.cancel();
      this.hideAnimation = null;
      this.hideToken++;
    }

    /**
     * Animates the page area into the newly picked tab. Returns animations the
     * HUD should wait for before it hides.
     */
    playTransition(target, item) {
      this.cancelTransition();
      const mode = Settings.transition;
      if (mode === "none" || prefersReducedMotion(this.win) || this.doc.fullscreenElement) {
        return [];
      }
      const splitView =
        target.hasAttribute("split-view") ||
        target.group?.hasAttribute?.("split-view-group") ||
        this.gBrowser.tabpanels?.getAttribute("zen-split-view") === "true";
      // Split views have their own layout, so a single preview can't land on it.
      if (mode === "zoom" && item && !splitView) {
        const animations = this.zoomInto(target, item);
        if (animations) {
          return animations;
        }
      }
      this.fadeContent(item ? 1 : 0.6);
      return [];
    }

    /** Soft blur-dissolve of the page area. `strength` 0..1 */
    fadeContent(strength) {
      const panels = this.gBrowser.tabpanels;
      if (!panels?.animate) {
        return;
      }
      const animation = panels.animate(
        [
          {
            opacity: 1 - 0.65 * strength,
            filter: `blur(${Math.round(12 * strength)}px)`,
            transform: `scale(${1 - 0.015 * strength})`,
          },
          { opacity: 1, filter: "blur(0px)", transform: "scale(1)" },
        ],
        { duration: Math.round(180 + 120 * strength), easing: "cubic-bezier(0.2, 0.8, 0.2, 1)" }
      );
      animation.id = ID;
    }

    /** Mission Control-style: the picked preview grows into the page area. */
    zoomInto(target, item) {
      const thumb = item._thumb;
      const hiRes = this.hiRes?.tab === target ? this.hiRes.canvas : null;
      const shot = hiRes || [...thumb.querySelectorAll(".mts-shot")].pop();
      const box = this.contentBox();
      const panels = this.gBrowser.tabpanels;
      if (!shot || !box || !panels?.animate) {
        return null;
      }
      const from = thumb.getBoundingClientRect();
      const to = box.rect;
      if (!from.width || !from.height) {
        return null;
      }
      const win = this.win;
      const fromRadius = parseFloat(win.getComputedStyle(thumb).borderTopLeftRadius) || 0;
      const toRadius = parseFloat(win.getComputedStyle(box.container).borderTopLeftRadius) || 0;
      const sx = from.width / to.width;
      const sy = from.height / to.height;

      const ghost = el(this.doc, "div", { class: "mts-ghost" });
      Object.assign(ghost.style, {
        left: `${to.left}px`,
        top: `${to.top}px`,
        width: `${to.width}px`,
        height: `${to.height}px`,
        borderRadius: `${toRadius}px`,
      });
      ghost.append(shot);
      thumb.style.visibility = "hidden";
      this.root.append(ghost);

      const move = ghost.animate(
        [
          {
            transform: `translate(${from.left - to.left}px, ${from.top - to.top}px) scale(${sx}, ${sy})`,
            borderRadius: `${fromRadius / sx}px / ${fromRadius / sy}px`,
          },
          { transform: "none", borderRadius: `${toRadius}px / ${toRadius}px` },
        ],
        { duration: SPRING_ZOOM.duration, easing: SPRING_ZOOM.easing }
      );
      const fade = ghost.animate(
        [{ opacity: 1 }, { opacity: 1, offset: 0.55 }, { opacity: 0 }],
        { duration: 380, fill: "forwards" }
      );
      // Keep the real page hidden while the preview flies in, then cross-fade.
      const reveal = panels.animate(
        [{ opacity: 0 }, { opacity: 0, offset: 0.45 }, { opacity: 1 }],
        { duration: 380, easing: "ease-out" }
      );
      reveal.id = ID;
      return [move, fade];
    }

    cancelTransition() {
      for (const animation of this.gBrowser?.tabpanels?.getAnimations?.() || []) {
        if (animation.id === ID) {
          animation.cancel();
        }
      }
      this.clearGhosts();
    }

    clearGhosts() {
      for (const ghost of this.root?.querySelectorAll(".mts-ghost") || []) {
        ghost.remove();
      }
    }

    /* -------------------------------------------------------- thumbnails */

    pruneThumbs() {
      // Only keep previews for tabs that can show up now (bounded memory).
      const keep = new Set(this.tabs);
      for (const tab of this.thumbs.keys()) {
        if (!keep.has(tab)) {
          this.thumbs.delete(tab);
        }
      }
    }

    /**
     * Captures previews for the current session's tabs. Runs once per session
     * (started during the hold delay); results land in the cache and, once
     * the HUD is visible, directly in their cards.
     */
    loadThumbnails(session) {
      if (this.loading?.session === session) {
        return this.loading.promise;
      }
      this.pruneThumbs();
      const size =
        this.state === "open" && this.metrics
          ? { w: this.metrics.cardW, h: this.metrics.cardH }
          : this.predictCardSize();
      const queue = this.tabs.slice();
      const worker = async () => {
        while (queue.length && session === this.session) {
          const tab = queue.shift();
          if (!tab.isConnected || tab.closing) {
            continue;
          }
          const url = tabURL(tab);
          const cached = this.thumbs.get(tab);
          const targetWidth = Math.round(size.w * this.win.devicePixelRatio);
          if (
            cached &&
            cached.url === url &&
            !tab.selected &&
            Date.now() - cached.time < THUMB_MAX_AGE_MS &&
            Math.abs(cached.width - targetWidth) / targetWidth < 0.2
          ) {
            continue;
          }
          let node = null;
          try {
            if (!tab.hasAttribute("pending")) {
              node = await this.capture(tab, size.w, size.h);
            } else if (!cached) {
              node = await this.loadStoredThumbnail(url);
            }
          } catch {
            // Pages can navigate or close mid-capture; keep the placeholder.
          }
          if (!node || session !== this.session) {
            continue;
          }
          this.thumbs.set(tab, { node, url, time: Date.now(), width: node.width || targetWidth });
          const item = this.items[this.tabs.indexOf(tab)];
          if (this.state === "open" && item?._tab === tab) {
            this.showShot(item, node);
          }
        }
      };
      const promise = Promise.all(Array.from({ length: CAPTURE_CONCURRENCY }, worker));
      this.loading = { session, promise };
      return promise;
    }

    /** Card size layout() will pick, so prefetched previews fit without rescaling. */
    predictCardSize() {
      const saved = this.items;
      this.items = this.tabs; // layout() only needs the count
      try {
        this.layout();
        return { w: this.metrics.cardW, h: this.metrics.cardH };
      } finally {
        this.items = saved;
        this.metrics = null;
      }
    }

    /**
     * A sharper capture of the highlighted tab so the zoom doesn't look soft.
     * Debounced: while cycling quickly nothing is captured, and the zoom
     * falls back to the card's preview.
     */
    scheduleHiRes() {
      this.win.clearTimeout(this.hiResTimer);
      const session = this.session;
      const tab = this.tabs[this.index];
      this.hiResTimer = this.win.setTimeout(() => this.captureHiRes(session, tab), 180);
    }

    async captureHiRes(session, tab) {
      if (
        session !== this.session ||
        this.tabs[this.index] !== tab ||
        Settings.transition !== "zoom" ||
        !tab ||
        tab.selected ||
        tab.hasAttribute("pending") ||
        this.hiRes?.tab === tab ||
        this.hiResInFlight === tab
      ) {
        return;
      }
      const box = this.contentBox();
      if (!box) {
        return;
      }
      this.hiResInFlight = tab;
      try {
        const canvas = await this.capture(tab, box.rect.width * 0.5, box.rect.height * 0.5);
        if (canvas && session === this.session) {
          this.hiRes = { tab, canvas };
        }
      } catch {
        // keep using the card preview
      } finally {
        if (this.hiResInFlight === tab) {
          this.hiResInFlight = null;
        }
      }
    }

    /** Snapshot of what is currently visible in the tab (scroll position included). */
    async capture(tab, cssWidth, cssHeight) {
      const browser = tab.linkedBrowser;
      const windowGlobal = browser?.browsingContext?.currentWindowGlobal;
      if (!windowGlobal) {
        return null;
      }
      let info = null;
      try {
        info = await windowGlobal
          .getActor("Thumbnails")
          .sendQuery("Browser:Thumbnail:ContentInfo");
      } catch {}
      if (!browser.isConnected) {
        return null;
      }
      const dpr = this.win.devicePixelRatio || 1;
      const canvas = el(this.doc, "canvas", { class: "mts-shot" });
      canvas.width = Math.max(1, Math.round(cssWidth * dpr));
      canvas.height = Math.max(1, Math.round(cssHeight * dpr));
      const aspect = canvas.width / canvas.height;
      const width = info?.width || this.contentBox()?.rect.width || 1280;
      const height = width / aspect;
      let y = info?.scrollY || 0;
      if (info?.documentHeight && info.documentHeight - y < height) {
        y = Math.max(0, info.documentHeight - height);
      }
      const bitmap = await browser.drawSnapshot(0, y, width, height, canvas.width / width, "white");
      if (!bitmap) {
        return null;
      }
      canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
      bitmap.close?.();
      return canvas;
    }

    /** Unloaded tabs can't be rendered; reuse a thumbnail Zen stored earlier, if any. */
    loadStoredThumbnail(url) {
      if (!/^https?:/.test(url)) {
        return Promise.resolve(null);
      }
      return new Promise(resolve => {
        const img = el(this.doc, "img", { class: "mts-shot", alt: "" });
        const timer = this.win.setTimeout(() => resolve(null), 1000);
        img.addEventListener("load", () => {
          this.win.clearTimeout(timer);
          resolve(img.naturalWidth ? img : null);
        });
        img.addEventListener("error", () => {
          this.win.clearTimeout(timer);
          resolve(null);
        });
        img.setAttribute("src", lazy.PageThumbs.getThumbnailURL(url));
      });
    }

    showShot(item, node) {
      const thumb = item._thumb;
      if (node.parentNode === thumb) {
        return;
      }
      const previous = [...thumb.querySelectorAll(".mts-shot")];
      thumb.append(node);
      const cleanup = () => {
        previous.forEach(old => old.remove());
        if (node.parentNode === thumb) {
          thumb.setAttribute("has-shot", ""); // stop painting the placeholder underneath
        }
      };
      if (prefersReducedMotion(this.win)) {
        cleanup();
        return;
      }
      node
        .animate([{ opacity: 0 }, { opacity: 1 }], { duration: 200, easing: "ease-out" })
        .finished.then(cleanup, cleanup);
    }
  }

  /* --------------------------------------------------------------- boot */

  function boot() {
    try {
      window.gZenMacTabSwitcher?.destroy();
    } catch {}
    const switcher = new MacTabSwitcher(window);
    switcher.init();
    window.gZenMacTabSwitcher = switcher;
  }

  if (window.gBrowserInit?.delayedStartupFinished) {
    boot();
  } else {
    const observer = (subject, topic) => {
      if (subject === window) {
        Services.obs.removeObserver(observer, topic);
        boot();
      }
    };
    Services.obs.addObserver(observer, "browser-delayed-startup-finished");
  }
})();
