// @vitest-environment jsdom
import { beforeEach, expect, it, vi } from "vitest";
import Files from "./Files.vue";

const { state, fetchFilesPublic, selectAndScrollToIndex } = vi.hoisted(() => ({
  state: {
    req: {},
    route: {},
    shareInfo: {},
    pendingHighlight: null,
    reload: false,
    user: { sorting: { field: "name", asc: true } },
    navigation: { isTransitioning: false },
  },
  fetchFilesPublic: vi.fn(),
  selectAndScrollToIndex: vi.fn(() => true),
}));

vi.mock("@/api/index.js", () => ({
  resourcesApi: { fetchFilesPublic },
  shareApi: { getShareInfoPublic: () => Promise.resolve({}) },
  mediaApi: {},
}));
vi.mock("@/store/index.ts", () => ({
  state,
  mutations: {
    setShareInfo: (info) => { state.shareInfo = info; },
    replaceRequest: (req) => { state.req = req; },
    setLoading: vi.fn(),
    setShareData: vi.fn(),
    resetSelected: vi.fn(),
    setMultiple: vi.fn(),
    setReload: vi.fn(),
  },
  getters: {
    shareHash: () => "example-share",
    routePath: () => "/example-share/",
  },
}));
vi.mock("@/router/index.ts", () => ({ router: {} }));
vi.mock("@/utils/listing/selectAndScroll.ts", () => ({ selectAndScrollToIndex }));
vi.mock("@/views/Errors.vue", () => ({ default: {} }));
vi.mock("@/views/files/Preview.vue", () => ({ default: {} }));
vi.mock("@/views/files/ListingView.vue", () => ({ default: {} }));
vi.mock("@/utils/asyncComponent.js", () => ({ createAsyncComponent: () => ({}) }));
vi.mock("@/utils/constants.ts", () => ({ globalVars: { name: "FileBrowser" } }));
vi.mock("@/utils/download.js", () => ({ showShareDownloadPrompt: vi.fn() }));

beforeEach(() => {
  vi.clearAllMocks();
  state.req = {};
  state.pendingHighlight = null;
  fetchFilesPublic.mockResolvedValue({
    name: "Shared folder",
    type: "directory",
    path: "/",
    source: "data",
    items: [
      { name: "notes", path: "/notes/", type: "directory" },
      { name: "资料 [A] #%20", path: "/资料 [A] #%20/", type: "directory" },
    ],
  });
});

async function returnToShare(previousHash, path) {
  state.req = { type: "directory", hash: previousHash, path, source: "data" };
  const view = {
    fetchId: 0,
    lastShareHash: previousHash,
    sharePassword: "",
    $route: { query: {} },
    $t: (key) => key,
    $nextTick: () => Promise.resolve(),
    ...Files.methods,
  };
  await view.fetchData();
}

it("highlights the child folder when returning to an ancestor in the same share", async () => {
  await returnToShare("example-share", "/资料 [A] #%20/nested/");
  expect(state.req.path).toBe("/");
  expect(selectAndScrollToIndex).toHaveBeenCalledOnce();
  expect(selectAndScrollToIndex).toHaveBeenCalledWith(1, true);
  expect(state.pendingHighlight).toBeNull();
});

it("does not reuse the previous folder after switching to a different share", async () => {
  await returnToShare("another-share", "/资料 [A] #%20/nested/");
  expect(state.req.path).toBe("/");
  expect(selectAndScrollToIndex).not.toHaveBeenCalled();
});

