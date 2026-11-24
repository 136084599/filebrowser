export function isPromptDescendant(prompts, id, ancestorId) {
  const seen = new Set();
  let prompt = prompts.find(entry => entry.id === id);
  while (typeof prompt?.parentId === 'number' && !seen.has(prompt.id)) {
    seen.add(prompt.id);
    if (prompt.parentId === ancestorId) return true;
    prompt = prompts.find(entry => entry.id === prompt.parentId);
  }
  return false;
}

export function promptRootId(prompts, id) {
  const seen = new Set();
  let prompt = prompts.find(entry => entry.id === id);
  while (typeof prompt?.parentId === 'number' && !seen.has(prompt.id)) {
    seen.add(prompt.id);
    const parent = prompts.find(entry => entry.id === prompt.parentId);
    if (!parent) break;
    prompt = parent;
  }
  return prompt?.id ?? id;
}

export function isPromptBlocked(prompts, prompt) {
  if (!prompt) return true;
  if (prompts.some(entry => entry.parentId === prompt.id)) return true;
  const pinned = [...prompts].reverse().find(entry => entry.pinned);
  return !!(pinned && pinned.id !== prompt.id && !isPromptDescendant(prompts, prompt.id, pinned.id));
}

export function insertPrompt(prompts, entry) {
  if (typeof entry.parentId === 'number' && prompts.some(prompt => prompt.id === entry.parentId)) {
    let index = prompts.findIndex(prompt => prompt.id === entry.parentId) + 1;
    while (index < prompts.length && isPromptDescendant(prompts, prompts.at(index)?.id, entry.parentId)) index++;
    return [...prompts.slice(0, index), entry, ...prompts.slice(index)];
  }
  const pinned = prompts.find(prompt => prompt.pinned);
  if (entry.pinned || !pinned) return [...prompts, entry];
  const index = prompts.findIndex(prompt => prompt.id === promptRootId(prompts, pinned.id));
  return [...prompts.slice(0, index), entry, ...prompts.slice(index)];
}

export function raisePrompt(prompts, id) {
  const prompt = prompts.find(entry => entry.id === id);
  if (!prompt || isPromptBlocked(prompts, prompt) || prompts.at(-1)?.id === id) return prompts;
  const rootId = promptRootId(prompts, id);
  const group = prompts.filter(entry => entry.id === rootId || isPromptDescendant(prompts, entry.id, rootId));
  const rest = prompts.filter(entry => !group.includes(entry));
  const pinned = rest.find(entry => entry.pinned);
  const index = pinned ? rest.findIndex(entry => entry.id === promptRootId(rest, pinned.id)) : rest.length;
  return [...rest.slice(0, index), ...group, ...rest.slice(index)];
}

export function withoutPromptTree(prompts, id) {
  return prompts.filter(entry => entry.id !== id && !isPromptDescendant(prompts, entry.id, id));
}
