import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createApp, nextTick } from 'vue';

const mocks = vi.hoisted(() => {
  const state = { prompts: [], req: { items: [] }, shareInfo: {} };
  return { state,
    getters: { currentPrompt: () => state.prompts.at(-1), sourcePermissions: () => ({ modify: true }), isShare: () => false, isPreviewView: () => false },
    mutations: { closeTopPrompt: vi.fn(), dismissPrompt: vi.fn() },
    resources: { moveCopy: vi.fn(), moveCopyPublic: vi.fn() },
  };
});
vi.mock('@/store', () => ({ state: mocks.state, getters: mocks.getters, mutations: mocks.mutations }));
vi.mock('@/api', () => ({ resourcesApi: mocks.resources }));
vi.mock('@/notify', () => ({ notify: { showError: vi.fn(), showSuccessToast: vi.fn() } }));
vi.mock('@/utils', () => ({ url: { goToItem: vi.fn() } }));
vi.mock('@/utils/files.js', () => ({ getFileExtension: () => '', removePrefix: value => value }));
vi.mock('@/store/eventBus', () => ({ eventBus: { emit: vi.fn() } }));
vi.mock('@/components/LoadingSpinner.vue', () => ({ default: { template: '<span />' } }));
import ReplaceRename from './ReplaceRename.vue';
import Rename from './Rename.vue';

let app;
function mount(component, props) {
  const element = document.createElement('div'); document.body.append(element);
  app = createApp(component, props); app.config.globalProperties.$t = key => key;
  app.mount(element); return element;
}
beforeEach(() => { vi.clearAllMocks(); mocks.state.prompts = []; mocks.state.req.items = []; });
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; });

describe('conflict dialog actions', () => {
  it('uses its own confirmation even if another prompt becomes current', async () => {
    const own = vi.fn(); const other = vi.fn();
    mocks.state.prompts = [{ id: 1, props: {}, confirm: own }, { id: 2, props: {}, confirm: other }];
    const element = mount(ReplaceRename, { promptId: 1 });
    element.querySelector('.button--red').click(); await nextTick();
    expect(own).toHaveBeenCalledTimes(1); expect(own.mock.calls[0][1]).toBe('overwrite');
    expect(other).not.toHaveBeenCalled();
  });
  it('does not start duplicate operations while a confirmation is pending', async () => {
    let finish;
    const own = vi.fn(() => new Promise(resolve => { finish = resolve; }));
    mocks.state.prompts = [{ id: 1, props: {}, confirm: own }];
    const element = mount(ReplaceRename, { promptId: 1 });
    const button = element.querySelector('.button--red');
    button.click(); button.click(); await nextTick();
    expect(own).toHaveBeenCalledTimes(1); expect(button.disabled).toBe(true);
    finish(); await Promise.resolve(); await nextTick();
    expect(button.disabled).toBe(false);
  });
  it('renames incoming folder selections without renaming existing server data', async () => {
    const confirm = vi.fn(() => true);
    mocks.state.prompts = [{ id: 1, confirm }];
    const element = mount(Rename, { promptId: 1, folderName: 'incoming-folder' });
    expect(element.querySelector('.filename-inputs')).toBeNull();
    const input = element.querySelector('input[aria-label="New Name"]');
    expect(input.value).toBe('incoming-folder');
    input.value = 'different-folder'; input.dispatchEvent(new Event('input', { bubbles: true })); await nextTick();
    element.querySelector('button[type="submit"]').click(); await Promise.resolve(); await nextTick();
    expect(confirm).toHaveBeenCalledWith('different-folder');
    expect(mocks.resources.moveCopy).not.toHaveBeenCalled();
    expect(mocks.mutations.closeTopPrompt).toHaveBeenCalledWith(1);
  });
  it('cancel addresses this rename dialog and does not close the unrelated top window', async () => {
    mocks.state.prompts = [{ id: 1 }, { id: 2 }];
    const element = mount(Rename, { promptId: 1, item: { name: 'folder', type: 'directory', source: 'Test', path: '/folder' } });
    expect(mocks.mutations.closeTopPrompt).not.toHaveBeenCalled();
    expect(mocks.mutations.dismissPrompt).not.toHaveBeenCalled();
    element.querySelector('.button--grey').click(); await nextTick();
    expect(mocks.mutations.dismissPrompt).toHaveBeenCalledWith(1);
  });
});
