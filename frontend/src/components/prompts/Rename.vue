<template>
  <div class="card-content">
    <!-- Loading spinner overlay -->
    <div v-show="renaming" class="loading-content">
      <LoadingSpinner size="small" />
      <p class="loading-text">{{ $t("prompts.operationInProgress") }}</p>
    </div>
    <div v-show="!renaming">
      <p>{{ $t("prompts.renameMessage") }}</p>

      <div v-if="renameItem.type !== 'directory'" class="filename-inputs">
      <input ref="filenameInput" aria-label="New Name" class="input" :class="{ 'form-invalid': !validation.valid }" v-focus type="text" @keydown="onKeydown" @keyup="onKeyup" v-model.trim="fileName" @input="updateFullName" />
      <span class="extension-separator">.</span> <!--eslint-disable-line @intlify/vue-i18n/no-raw-text-->
      <input class="input extension-input" type="text" @keydown="onKeydown" @keyup="onKeyup" v-model.trim="fileExtension" @input="updateFullName" />
    </div>

    <input v-else ref="directoryInput" class="input" aria-label="New Name" :class="{ 'form-invalid': !validation.valid }" v-focus type="text" @keydown="onKeydown" @keyup="onKeyup" v-model.trim="name" />
    <p v-if="!validation.valid && name.length > 0" class="validation-error">
      <span v-if="validation.reason === 'conflict'">
        {{ $t("prompts.renameMessageConflict", { filename: name }) }}
      </span>
      <span v-else>
        {{ $t("prompts.renameMessageInvalid") }}
      </span>
    </p>
    </div>
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
    <button @click="submit" class="button button--flat" :disabled="!canRename || renaming"
      type="submit" :aria-label="$t('general.submit')" :title="$t('general.submit')">
      {{ $t("general.rename") }}
    </button>
  </div>
</template>
<script>
import { resourcesApi } from "@/api";
import { mutations, state, getters } from "@/store";
import { eventBus } from '@/store/eventBus';
import { notify } from "@/notify";
import { getFileExtension, removePrefix } from '@/utils/files.js';
import { url } from "@/utils";
import LoadingSpinner from "@/components/LoadingSpinner.vue";
export default {
  name: "rename",
  components: {
    LoadingSpinner,
  },
  props: {
    promptId: { type: Number, default: null },
    folderName: { type: String, default: null },
    item: {
      type: Object,
      default: () => ({ source: "", path: "", name: "" })
    },
    parentItems: {  // Parent items for comparison of filenames in a preview
      type: Array,
      default: () => []
    }
  },
  data() {
    const itemName = this.folderName ?? this.item.name;
    let fileName = "";
    let fileExtension = "";
    if (this.folderName === null && this.item.type !== 'directory') {
      const ext = getFileExtension(this.item.name);
      const filenamePrefix = this.item.name.substring(0, this.item.name.length - ext.length);
      fileName = filenamePrefix;
      fileExtension = removePrefix(ext, ".");
    }
    return {
      fileName,
      fileExtension,
      name: itemName,
      renaming: false,
    };
  },
  computed: {
    renameItem() {
      return this.folderName === null ? this.item : { name: this.folderName, type: 'directory' };
    },
    validation() {
      return this.validateFileName(this.name);
    },
    canRename() {
      // Enable the button only if the name is different from the original, and if the filename field is not empty
      const hasValidFileName = this.renameItem.type === 'directory'
        ? this.name.trim() !== ''
        : this.fileName.trim() !== '';
      return this.validation.valid &&
             this.name !== this.renameItem.name &&
             hasValidFileName;
    },
    isPreviewView() {
      return getters.isPreviewView();
    },
  },
  mounted() {
    this.$nextTick(() => {
      // Auto-focus filename input field
      if (this.renameItem.type !== 'directory') {
        const filenameInput = this.$refs.filenameInput;
        filenameInput?.select();
      } else {
        const directoryInput = this.$refs.directoryInput;
        directoryInput?.select();
      }
    });
  },
  methods: {
    closeTopPrompt() { mutations.dismissPrompt(this.promptId ?? undefined); },
    onKeydown(event) {
      // Allow "esc" key to close prompt and block other shortcuts
      // e.g. in plyrViewer we have "P" and "L", without this we can't type those letters
    if (event.key !== "Escape") {
        event.stopPropagation();
      }
    },
    onKeyup(event) {
    if (event.key !== "Escape") {
        event.stopPropagation();
      }
      if (event.key === 'Enter') {
        void this.submit();
      }
    },
    updateFullName() {
      // Combine filename and extension
      if (this.renameItem.type !== 'directory') {
        this.name = this.fileExtension 
          ? `${this.fileName}.${this.fileExtension}`
          : this.fileName;
      }
    },
    /**
     * @param {string} value
     */
    validateFileName(value) {
      // Handle undefined, null, or empty values
      if (!value || value === "") {
        return { valid: true };
      }

      const isFolder = this.renameItem.type === "directory";

      // Check for forbidden characters in file names
      if (!isFolder) {
        const forbiddenChars = /[/\\]/;
        if (forbiddenChars.test(value)) {
          return { valid: false, reason: 'invalidChar' };
        }
      }

      // Get the current item's name for comparison
      const currentItemName = this.renameItem.name.toLowerCase();
      const newName = value.toLowerCase();

      // If renaming to the same name (case-insensitive), allow it
      if (currentItemName === newName) {
        return { valid: true };
      }

      // Use parentItems if we are in a preview, otherwise use state.req.items
      const items = this.parentItems.length > 0 ? this.parentItems : (state.req?.items ?? []);
      for (const item of items) {
        if (!item.name) {
          continue;
        }
        const itemName = item.name.toLowerCase();
        // Skip the current item by comparing names (case-insensitive)
        // This is more reliable than comparing paths as before
        if (itemName === currentItemName) {
          continue;
        }
        if (itemName === newName) {
          return { valid: false, reason: 'conflict' };
        }
      }

      return { valid: true };
    },
    normalizePath(p) {
      return (p || '').replace(/^\/+|\/+$/g, '');
    },
    async submit() {
      if (this.renaming) return;
      // remove trailing slashes
      if (this.name.endsWith("/") || this.name.endsWith("\\")) {
        this.name = this.name.substring(0, this.name.length - 1);
      }
      if (!this.validation.valid || !this.canRename) return;
      const ownerId = this.promptId ?? getters.currentPrompt()?.id;
      if (this.folderName !== null) {
        const prompt = state.prompts.find(entry => entry.id === ownerId);
        if (typeof prompt?.confirm !== 'function') return;
        this.renaming = true;
        try {
          if (await prompt.confirm(this.name) !== false) mutations.closeTopPrompt(ownerId);
        } catch (error) { notify.showError(error.message || String(error)); }
        finally { this.renaming = false; }
        return;
      }
      // Remove trailing slashes from the source path before calculating parent directory
      let sourcePath = this.item.path;
      while (sourcePath.endsWith("/") || sourcePath.endsWith("\\")) {
        sourcePath = sourcePath.substring(0, sourcePath.length - 1);
      }

      let newPath = sourcePath.substring(0, sourcePath.lastIndexOf("/"));
      newPath = `${newPath}/${this.name}`;
      this.renaming = true;
      try {
        const items = [{
          from: this.item.path,
          fromSource: this.item.source,
          to: newPath,
          toSource: this.item.source,
        }];

        if (getters.isShare()) {
          await resourcesApi.moveCopyPublic(state.shareInfo.hash, items, "move");
        } else {
          await resourcesApi.moveCopy(items, "move");
        }
        notify.showSuccessToast(this.$t("prompts.renameSuccess"));
        eventBus.emit('itemsRenamed');
        mutations.closeTopPrompt(ownerId);

        if (this.isPreviewView) {
          // Navigate only if we rename the file that we're currently previewing (eg: from fileTree)
          const currentReqPath = this.normalizePath(state.req?.path);
          const oldItemPath = this.normalizePath(this.item.path);
          
          // For shares compare path and for regulars compare both path and source
          const isCurrentItem = getters.isShare()
            ? currentReqPath === oldItemPath
            : (currentReqPath === oldItemPath && this.item.source === state.req?.source);
          
          if (isCurrentItem) {
            // Navigate to the renamed file
            const source = getters.isShare() ? state.shareInfo.hash : this.item.source;
            url.goToItem(source, newPath, undefined, false, getters.isShare()); // When undefined will not create browser history
          } else {
            mutations.setReload(true);
          }
        } else {
          mutations.setReload(true);
        }
      } catch (error) {
        console.error(error);
        // Parse the error response structure (similar to Delete.vue)
        let errorMessage = this.$t("prompts.renameFailed");
        
        if (error?.failed && error.failed.length > 0) {
          // Get message from first failed item
          errorMessage = error.failed[0].message || errorMessage;
        } else if (error?.message) {
          errorMessage = error.message;
        }
        
        notify.showError(errorMessage);
      } finally {
        this.renaming = false;
      }
    },
  },
};
</script>

<style scoped>
.form-invalid {
  border-color: var(--red) !important;
}

.validation-error {
  color: var(--red);
  font-size: 0.9em;
  margin-top: 0.5em;
  margin-bottom: 0;
}

.button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.filename-inputs {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.extension-input {
  width: 6em;
}

.extension-separator {
  font-weight: bold;
}

.loading-content {
  text-align: center;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 16px;
  padding-top: 2em;
}

.loading-text {
  padding: 1em;
  margin: 0;
  font-size: 1em;
  font-weight: 500;
}

.card-content {
  position: relative;
}
</style>
