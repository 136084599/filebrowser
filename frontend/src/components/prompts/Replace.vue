<template>
  <div class="card-content">
    <p>{{ $t("prompts.replaceMessage") }}</p>
    <p v-if="busy" role="status">{{ $t('prompts.operationInProgress') }}</p>
  </div>

  <div class="card-actions">
    <button
      type="button"
      class="button button--flat button--grey"
      @click="closeTopPrompt" :aria-label="$t('general.cancel')"
      :title="$t('general.cancel')"
    >
      {{ $t("general.cancel") }}
    </button>
    <button
      type="button"
      class="button button--flat button--red"
      @click="confirm"
      :disabled="busy"
      :aria-label="$t('general.replace')"
      :title="$t('general.replace')"
    >
      {{ $t("general.replace") }}
    </button>
  </div>
</template>
<script>
import { getters, mutations, state } from "@/store";
import { notify } from '@/notify';

export default {
  name: "replace",
  props: { promptId: { type: Number, default: null } },
  data: () => ({ busy: false }),
  computed: {
    currentPrompt() {
      return this.promptId === null ? getters.currentPrompt() : state.prompts.find(prompt => prompt.id === this.promptId);
    },
  },
  methods: {
    closeTopPrompt() {
      mutations.dismissPrompt(this.promptId ?? undefined);
    },
    async confirm(event) {
      if (this.busy || typeof this.currentPrompt?.confirm !== 'function') return;
      this.busy = true;
      try { await this.currentPrompt.confirm(event); }
      catch (error) { notify.showError(error.message || String(error)); }
      finally { this.busy = false; }
    },
  },
};
</script>
