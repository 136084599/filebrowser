<template>
  <div class="card-content">
    <template v-if="allowSkip">
      <p>{{ $t("prompts.uploadConflictMessage") }}</p>
      <p>
        <strong>{{ $t("prompts.optionLabel", { name: $t("prompts.uploadMissing") }) }}</strong>
        {{ $t("prompts.uploadMissingInfo") }}
      </p>
      <p v-if="canReplace">
        <strong>{{ $t("prompts.optionLabel", { name: $t("general.replace") }) }}</strong>
        {{ $t("prompts.uploadReplaceInfo") }}
      </p>
      <p v-if="allowRename">
        <strong>{{ $t("prompts.optionLabel", { name: $t("general.rename") }) }}</strong>
        {{ $t("prompts.uploadRenameInfo") }}
      </p>
    </template>
    <p v-else>{{ $t("prompts.replaceMessage") }}</p>
    <p v-if="busy" role="status">{{ $t('prompts.operationInProgress') }}</p>
  </div>
  <div class="card-actions">
    <button
      v-if="allowRename"
      type="button"
      class="button button--flat button--blue"
      :id="canReplace ? undefined : 'focus-prompt'"
      @click="choose($event, 'rename')"
      :disabled="busy"
      :aria-label="$t('general.rename')"
      :title="$t('general.rename')"
    >
      {{ $t("general.rename") }}
    </button>
    <button
      v-if="allowSkip"
      type="button"
      class="button button--flat button--blue"
      @click="choose($event, 'skip')"
      :disabled="busy"
      :aria-label="$t('prompts.uploadMissing')"
      :title="$t('prompts.uploadMissing')"
    >
      {{ $t("prompts.uploadMissing") }}
    </button>
    <button
      v-if="canReplace"
      type="button"
      id="focus-prompt"
      class="button button--flat button--red"
      :disabled="isSameFile || busy"
      @click="choose($event, 'overwrite')"
      :aria-label="$t('general.replace')"
      :title="$t('general.replace')">
      {{ $t("general.replace") }}
    </button>
  </div>
</template>

<script>
import { getters, state } from "@/store";
import { notify } from '@/notify';

export default {
  name: "replace-rename",
  props: { promptId: { type: Number, default: null } },
  data: () => ({ busy: false }),
  computed: {
    currentPrompt() {
      return this.promptId === null ? getters.currentPrompt() : state.prompts.find(prompt => prompt.id === this.promptId);
    },
    allowRename() {
      return this.currentPrompt?.props?.allowRename !== false;
    },
    allowSkip() {
      return this.currentPrompt?.props?.allowSkip === true;
    },
    isSameFile() {
      return this.currentPrompt?.props?.isSameFile === true;
    },
    canReplace() {
      const canModify = !!getters.sourcePermissions().modify;
      if (getters.isShare()) {
        return canModify && !!state.shareInfo?.allowReplacements;
      }
      return canModify;
    },
  },
  methods: {
    async choose(event, option) {
      event.preventDefault();
      if (this.busy || typeof this.currentPrompt?.confirm !== 'function') return;
      this.busy = true;
      try { await this.currentPrompt.confirm(event, option); }
      catch (error) { notify.showError(error.message || String(error)); }
      finally { this.busy = false; }
    },
  },
};
</script>
