import { afterEach, describe, expect, it, vi } from 'vitest';
import { captureDirectoryPosition, clearDirectoryHighlight, directoryContext, directoryReturnPosition, restoreDirectoryPosition } from './directoryNavigation';

let userId = 100;
const context = (path, source = 'Test', user = userId, share = '') => directoryContext({ path, source, type: 'directory' }, { id: user }, share);
afterEach(() => { userId++; clearDirectoryHighlight(); document.body.innerHTML = ''; vi.useRealTimers(); });

describe('returning to directories', () => {
  it('remembers every parent across multiple levels, with source and account isolation', () => {
    captureDirectoryPosition(context('/'), { scrollTop: 600, scrollLeft: 0 });
    captureDirectoryPosition(context('/素材'), { scrollTop: 1200, scrollLeft: 0 });
    captureDirectoryPosition(context('/素材/采访'), { scrollTop: 2400, scrollLeft: 0 });
    expect(directoryReturnPosition(context('/素材'))).toMatchObject({ name: '采访', top: 1200 });
    captureDirectoryPosition(context('/素材'), { scrollTop: 1200, scrollLeft: 0 });
    expect(directoryReturnPosition(context('/'))).toMatchObject({ name: '素材', top: 600 });
    expect(directoryReturnPosition(context('/', 'Other'))).toEqual({ name: '' });
    expect(directoryReturnPosition(context('/', 'Test', userId + 1))).toEqual({ name: '' });
  });
  it('locates the containing folder when breadcrumbs skip several levels', () => {
    captureDirectoryPosition(context('/根/甲/乙/丙'), null);
    expect(directoryReturnPosition(context('/根'))).toEqual({ name: '甲' });
    expect(directoryReturnPosition(context('/根/甲乙'))).toEqual({ name: '' });
  });
  it('keeps share locations separate and honors explicit hashes literally', () => {
    captureDirectoryPosition(context('/视频', 'Source', userId, 'share-one'), null);
    expect(directoryReturnPosition(context('/', 'Source', userId, 'share-two'))).toEqual({ name: '' });
    expect(directoryReturnPosition(context('/'), '#%E4%B8%AD%E6%96%87%2520%23')).toEqual({ name: '中文%20#' });
    expect(directoryReturnPosition(context('/'), '#bad%escape')).toEqual({ name: 'bad%escape' });
  });
  it('restores forward navigation to a visited child without marking an unrelated entry', () => {
    captureDirectoryPosition(context('/child'), { scrollTop: 800, scrollLeft: 30 });
    captureDirectoryPosition(context('/'), { scrollTop: 200, scrollLeft: 0 });
    expect(directoryReturnPosition(context('/child/'))).toEqual({ name: '', top: 800, left: 30 });
  });
  it('handles a renamed or deleted target by preserving the remembered scroll position', () => {
    captureDirectoryPosition(context('/'), { scrollTop: 700, scrollLeft: 0 });
    captureDirectoryPosition(context('/deleted'), null);
    const root = document.createElement('div');
    const container = { scrollTop: 0, scrollLeft: 0 };
    expect(restoreDirectoryPosition(context('/'), container, root)).toBe('');
    expect(container.scrollTop).toBe(700);
  });
  it('finds special names, brings them into view, and marks location without selecting for file operations', () => {
    vi.useFakeTimers();
    const name = '采访%20 #" [一]';
    captureDirectoryPosition(context('/'), { scrollTop: 300, scrollLeft: 0 });
    captureDirectoryPosition(context(`/${name}`), null);
    const root = document.createElement('div'); root.style.paddingTop = '100px';
    const item = document.createElement('a'); item.className = 'listing-item'; item.tabIndex = 0; item.dataset.name = name;
    root.append(item); document.body.append(root);
    item.getBoundingClientRect = () => ({ top: 1200, bottom: 1260, height: 60 });
    const container = { scrollTop: 0, scrollLeft: 0, getBoundingClientRect: () => ({ top: 0, bottom: 800 }) };
    expect(restoreDirectoryPosition(context('/'), container, root)).toBe(name);
    expect(container.scrollTop).toBeGreaterThan(300);
    expect(item.classList.contains('navigation-returned')).toBe(true);
    expect(item.getAttribute('aria-selected')).toBeNull();
    expect(document.activeElement).toBe(item);
    vi.advanceTimersByTime(4000);
    expect(item.classList.contains('navigation-returned')).toBe(false);
  });
  it('bounds tab memory to the most recently visited 100 directories', () => {
    for (let i = 0; i < 101; i++) captureDirectoryPosition(context(`/folder-${i}`), { scrollTop: i, scrollLeft: 0 });
    expect(directoryReturnPosition(context('/folder-0')).top).toBeUndefined();
    expect(directoryReturnPosition(context('/folder-100')).top).toBe(100);
  });
  it('does not break navigation away from an error or still-empty container', () => {
    const main = document.createElement('main');
    expect(() => captureDirectoryPosition(context('/'), main)).not.toThrow();
  });
  it('focuses the saved anchor on Forward without marking it as a returned child', () => {
    const main = document.createElement('main');
    main.getBoundingClientRect = () => ({ top: 0, bottom: 800 });
    const root = document.createElement('div'); root.className = 'listing-view-root';
    const item = document.createElement('a'); item.className = 'listing-item'; item.dataset.name = 'anchor'; item.tabIndex = 0;
    item.getBoundingClientRect = () => ({ top: 200, bottom: 260, height: 60 });
    root.append(item); main.append(root); document.body.append(main); item.focus();
    captureDirectoryPosition(context('/child'), main);
    captureDirectoryPosition(context('/'), null); item.blur();
    expect(restoreDirectoryPosition(context('/child'), main, root)).toBe('');
    expect(document.activeElement).toBe(item);
    expect(item.classList.contains('navigation-returned')).toBe(false);
  });
});
