// Behavior tests for zen-mac-tab-switcher.uc.js in jsdom, with small stubs for
// the Zen/Gecko APIs it uses. Run: npm test (from this folder).
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

const SCRIPT = process.argv[2] || path.join(__dirname, "..", "..", "zen-mac-tab-switcher.uc.js");
const src = fs
  .readFileSync(SCRIPT, "utf8")
  .replace(`window.location.href !== "chrome://browser/content/browser.xhtml"`, "false")
  .replaceAll("event.isTrusted", "true"); // jsdom events are untrusted

function setup({ prefs = {}, platform = "linux" } = {}) {
  const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", {
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const w = dom.window;
  const doc = w.document;
  const anim = () => ({ finished: Promise.resolve(), cancel() {}, id: "" });
  w.Element.prototype.animate = anim;
  w.Element.prototype.getAnimations = () => [];
  w.matchMedia = q => ({ matches: false, media: q });
  w.windowUtils = { layerManagerType: "WebRender" };

  const prefStore = { ...prefs };
  const getter = (n, d) => (n in prefStore ? prefStore[n] : d);
  w.Services = {
    prefs: { getBoolPref: getter, getIntPref: getter, getStringPref: getter },
    obs: { addObserver() {}, removeObserver() {} },
  };
  w.AppConstants = { platform };
  w.ChromeUtils = {
    defineESModuleGetters(obj) {
      obj.PageThumbs = { getThumbnailURL: u => "moz-page-thumb://" + u };
      obj.PrivateBrowsingUtils = { isWindowPrivate: () => false };
      obj.ShortcutUtils = {
        CYCLE_TABS: "cycle",
        getSystemActionForEvent: e => (e.key === "Tab" && e.ctrlKey && !e.altKey ? "cycle" : null),
      };
    },
    importESModule() {
      throw new Error("no module");
    },
  };

  const container = doc.createElement("div");
  const essentials = doc.createElement("div"); // Zen keeps these outside tabContainer
  const panels = doc.createElement("div");
  doc.body.append(essentials, container, panels);
  const tabs = [];
  let selected = null;
  const mkTab = (label, accessed, parent = container) => {
    const t = doc.createElement("div");
    t.label = label;
    t.image = "";
    t.lastAccessed = accessed;
    t.linkedBrowser = { currentURI: null, isConnected: true };
    t.pinned = false;
    Object.defineProperty(t, "selected", { get: () => t === selected });
    parent.append(t);
    tabs.push(t);
    return t;
  };
  const gBrowser = {
    get tabs() {
      return tabs.filter(t => t.isConnected);
    },
    get selectedTab() {
      return selected;
    },
    set selectedTab(t) {
      selected = t;
      t.lastAccessed = Date.now() + 1e6;
    },
    tabContainer: container,
    tabpanels: panels,
    warmupTab() {},
    removeTab(t) {
      t.dispatchEvent(new w.Event("TabClose", { bubbles: true }));
      t.remove();
    },
  };
  w.gBrowser = gBrowser;
  w.gBrowserInit = { delayedStartupFinished: true };
  return { w, doc, gBrowser, mkTab, essentials, select: t => (selected = t), prefStore };
}

const key = (w, type, init) =>
  w.dispatchEvent(new w.KeyboardEvent(type, { bubbles: true, cancelable: true, ...init }));
const ctrlTab = (w, shift = false) => key(w, "keydown", { key: "Tab", code: "Tab", ctrlKey: true, shiftKey: shift });
const releaseCtrl = w => key(w, "keyup", { key: "Control", code: "ControlLeft", ctrlKey: false });
const sleep = ms => new Promise(r => setTimeout(r, ms));

function world(opts) {
  const env = setup(opts);
  const a = env.mkTab("A", 1000);
  const b = env.mkTab("B", 3000);
  const c = env.mkTab("C", 2000);
  const d = env.mkTab("D", 500);
  env.select(a);
  env.w.eval(src);
  return { ...env, a, b, c, d, sw: env.w.gZenMacTabSwitcher };
}

const tests = {
  "quick tap switches to the most recent tab without showing the HUD"() {
    const { w, gBrowser, b, sw } = world();
    ctrlTab(w);
    assert.equal(sw.state, "pending");
    releaseCtrl(w);
    assert.equal(gBrowser.selectedTab, b);
    assert.equal(sw.state, "idle");
  },
  async "hold shows the HUD in MRU order and Tab moves the selection"() {
    const { w, gBrowser, a, b, c, sw } = world();
    ctrlTab(w);
    await sleep(200);
    assert.equal(sw.state, "open");
    assert.deepEqual(sw.tabs.map(t => t.label), ["A", "B", "C", "D"]);
    assert.equal(sw.items.length, 4);
    ctrlTab(w);
    assert.equal(sw.index, 2);
    releaseCtrl(w);
    assert.equal(gBrowser.selectedTab, c);
  },
  "Ctrl+Shift+Tab starts at the last tab"() {
    const { w, gBrowser, d } = world();
    ctrlTab(w, true);
    releaseCtrl(w);
    assert.equal(gBrowser.selectedTab, d);
  },
  async "Esc cancels"() {
    const { w, gBrowser, a, sw } = world();
    ctrlTab(w);
    await sleep(200);
    key(w, "keydown", { key: "Escape", code: "Escape", ctrlKey: true });
    assert.equal(sw.state, "idle");
    releaseCtrl(w);
    assert.equal(gBrowser.selectedTab, a);
  },
  async "W closes the highlighted tab and keeps the HUD open"() {
    const { w, b, sw } = world();
    ctrlTab(w);
    await sleep(200);
    key(w, "keydown", { key: "w", code: "KeyW", ctrlKey: true });
    assert.equal(b.isConnected, false);
    assert.deepEqual(sw.tabs.map(t => t.label), ["A", "C", "D"]);
    assert.equal(sw.items.length, 3);
    assert.equal(sw.state, "open");
  },
  async "W on a non-Latin layout (physical KeyW) closes the tab"() {
    const { w, b } = world();
    ctrlTab(w);
    await sleep(200);
    key(w, "keydown", { key: "ц", code: "KeyW", ctrlKey: true });
    assert.equal(b.isConnected, false);
  },
  async "closing an Essentials tab outside tabContainer updates the HUD"() {
    const env = setup();
    const a = env.mkTab("A", 1000);
    const e = env.mkTab("Essential", 3000, env.essentials);
    env.mkTab("C", 2000);
    env.select(a);
    env.w.eval(src);
    const sw = env.w.gZenMacTabSwitcher;
    ctrlTab(env.w);
    await sleep(200);
    assert.equal(sw.tabs[1], e);
    env.gBrowser.removeTab(e);
    assert.ok(!sw.tabs.includes(e), "closed Essentials tab still listed");
    assert.equal(sw.items.length, 2);
  },
  async "right click on Linux does not switch"() {
    const { w, gBrowser, a, sw } = world();
    ctrlTab(w);
    await sleep(200);
    const item = sw.items[2];
    item.dispatchEvent(new w.MouseEvent("mouseup", { bubbles: true, button: 2 }));
    assert.equal(sw.state, "open");
    item.dispatchEvent(new w.MouseEvent("mouseup", { bubbles: true, button: 0 }));
    assert.equal(gBrowser.selectedTab, sw.tabs[2] || gBrowser.selectedTab);
    assert.notEqual(gBrowser.selectedTab, a);
  },
  async "Ctrl+click (button 2) on macOS switches"() {
    const { w, gBrowser, a, sw } = world({ platform: "macosx" });
    ctrlTab(w);
    await sleep(200);
    sw.items[3].dispatchEvent(new w.MouseEvent("mouseup", { bubbles: true, button: 2 }));
    assert.equal(sw.state, "idle");
    assert.equal(gBrowser.selectedTab.label, "D");
  },
  "disabled pref leaves Ctrl+Tab alone"() {
    const { w, gBrowser, a, sw } = world({ prefs: { "zen.mac-tab-switcher.enabled": false } });
    const notCanceled = ctrlTab(w);
    assert.equal(notCanceled, true);
    assert.equal(sw.state, "idle");
    assert.equal(gBrowser.selectedTab, a);
  },
  "unload removes listeners and the HUD"() {
    const { w, doc, sw } = world();
    w.dispatchEvent(new w.Event("unload"));
    assert.equal(doc.getElementById("zen-mac-tab-switcher"), null);
    assert.equal(w.gZenMacTabSwitcher, undefined);
    ctrlTab(w);
    assert.equal(sw.state, "idle");
  },
};

(async () => {
  let failed = 0;
  for (const [name, fn] of Object.entries(tests)) {
    try {
      await fn();
      console.log("PASS", name);
    } catch (e) {
      failed++;
      console.log("FAIL", name, "\n   ", e.message);
    }
  }
  console.log(failed ? `${failed} failed` : "all passed");
  process.exit(failed ? 1 : 0);
})();
