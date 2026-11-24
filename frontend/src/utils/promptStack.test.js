import { describe, expect, it } from 'vitest';
import { insertPrompt, isPromptBlocked, isPromptDescendant, raisePrompt, withoutPromptTree } from './promptStack';

describe('modal parent and child ordering', () => {
  it('keeps an unpinned child above a pinned parent and accepts its input', () => {
    const parent = { id: 1, pinned: true };
    const child = { id: 2, parentId: 1 };
    const stack = insertPrompt([parent], child);
    expect(stack).toEqual([parent, child]);
    expect(isPromptBlocked(stack, parent)).toBe(true);
    expect(isPromptBlocked(stack, child)).toBe(false);
  });
  it('keeps the deepest child interactive through nested pinned dialogs', () => {
    const stack = [{ id: 1 }, { id: 2, parentId: 1, pinned: true }, { id: 3, parentId: 2 }];
    expect(stack.map(prompt => isPromptBlocked(stack, prompt))).toEqual([true, true, false]);
  });
  it('inserts independent background windows below the entire pinned family', () => {
    const stack = [{ id: 1 }, { id: 2, pinned: true, parentId: 1 }];
    expect(insertPrompt(stack, { id: 3, parentId: null }).map(prompt => prompt.id)).toEqual([3, 1, 2]);
  });
  it('inserts a background child below an unrelated pinned family', () => {
    const stack = [{ id: 1 }, { id: 2, pinned: true, parentId: null }];
    const result = insertPrompt(stack, { id: 3, parentId: 1 });
    expect(result.map(prompt => prompt.id)).toEqual([1, 3, 2]);
    expect(isPromptBlocked(result, result[1])).toBe(true);
    expect(isPromptBlocked(result, result[2])).toBe(false);
  });
  it('does not mutate or reorder the top dialog during mousedown', () => {
    const stack = [{ id: 1 }, { id: 2, parentId: 1 }];
    expect(raisePrompt(stack, 2)).toBe(stack);
  });
  it('raises a window family together without putting a parent over its child', () => {
    const stack = [{ id: 1 }, { id: 2, parentId: 1 }, { id: 3, parentId: null }];
    expect(raisePrompt(stack, 2).map(prompt => prompt.id)).toEqual([3, 1, 2]);
    expect(raisePrompt(stack, 1)).toBe(stack);
  });
  it('closing an owner removes all descendants but preserves unrelated windows', () => {
    const stack = [{ id: 1 }, { id: 2, parentId: 1, pinned: true }, { id: 3, parentId: 2 }, { id: 4, parentId: null }];
    expect(withoutPromptTree(stack, 1)).toEqual([{ id: 4, parentId: null }]);
    expect(withoutPromptTree(stack, 2)).toEqual([{ id: 1 }, { id: 4, parentId: null }]);
  });
  it('handles stale and cyclic parents without looping', () => {
    expect(isPromptDescendant([{ id: 1, parentId: 2 }, { id: 2, parentId: 1 }], 1, 99)).toBe(false);
    expect(isPromptBlocked([], null)).toBe(true);
  });
});
