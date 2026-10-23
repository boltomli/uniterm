<template>
  <div class="redis-tab-content">
    <div class="redis-main" :class="{ 'm-tree-host': isNarrowScreen }">
      <!-- Left Panel: Key List -->
      <div v-if="isNarrowScreen && treeOpen" class="m-tree-overlay" @click="treeOpen = false" />
      <div
        v-show="isNarrowScreen || treeOpen"
        class="redis-left"
        :class="{ 'm-tree': isNarrowScreen, 'm-tree-open': isNarrowScreen && treeOpen }"
        :style="isNarrowScreen ? {} : { width: leftWidth + 'px' }"
      >
        <!-- Controls row -->
        <div class="redis-toolbar">
          <el-select v-model="currentDb" size="small" style="width: 5.625rem; flex-shrink: 0" @change="onSwitchDB">
            <el-option v-for="n in 16" :key="n-1" :label="`${n-1} (${dbSizes[n-1] ?? '?'})`" :value="n-1" />
          </el-select>
          <el-input v-model="scanPattern" size="small" placeholder="*" style="flex: 1; min-width: 5.0rem" @keyup.enter="onScan" />
          <button class="btn btn-ghost btn-icon btn-sm" :title="t('redis.refresh')" @click="onScan" style="flex-shrink: 0"><RefreshCw :size="lucideSize('0.875rem')" /></button>
          <button class="btn btn-ghost btn-icon btn-sm" :title="treeMode ? t('redis.flatView') : t('redis.treeView')" @click="treeMode = !treeMode" style="flex-shrink: 0">
            <FolderTree :size="lucideSize('0.875rem')" v-if="!treeMode" />
            <List :size="lucideSize('0.875rem')" v-else />
          </button>
          <button class="btn btn-ghost btn-icon btn-sm" :title="t('redis.newKey')" @click="onShowNewKeyDialog" style="flex-shrink: 0"><Plus :size="lucideSize('0.875rem')" /></button>
        </div>

        <!-- Key tree / flat list — P1: RecycleScroller 虚拟滚动，避免大结果集全量 DOM -->
        <div class="redis-key-list" v-loading="loading">
          <div v-if="keys.length === 0 && !loading" class="redis-placeholder">{{ t('redis.noKeys') }}</div>
          <RecycleScroller
            v-else
            :items="listItems"
            :item-size="28"
            :key-field="listKeyField"
            class="redis-scroller"
            v-slot="{ item }"
          >
            <!-- 树形模式：folder / key 行 -->
            <template v-if="treeMode">
              <div
                v-if="item.node.children.length > 0"
                class="db-header"
                :style="{ paddingLeft: (8 + item.depth * 18) + 'px' }"
                @click="onToggleFolder(item.node.id)"
              >
                <span class="db-arrow" @click.stop="onToggleFolder(item.node.id)">
                  <component :is="expandedFolders.has(item.node.id) ? ChevronDown : ChevronRight" :size="lucideSize('0.75rem')" />
                </span>
                <Folder class="db-icon" :size="lucideSize('0.875rem')" />
                <span class="db-name">{{ item.node.label }}</span>
              </div>
              <div
                v-else
                class="table-item"
                :class="{ selected: selectedKey === item.node.keyName }"
                :style="{ paddingLeft: (8 + item.depth * 18) + 'px' }"
                @click="onSelectTreeKey(item.node)"
              >
                <span class="table-icon-spacer" />
                <component :is="typeIcon(item.node.keyType)" class="table-icon" :size="lucideSize('0.875rem')" />
                <span class="table-name">{{ item.node.label }}</span>
              </div>
            </template>
            <!-- 扁平模式：key 行 -->
            <div
              v-else
              class="table-item"
              :class="{ selected: selectedKey === item.name }"
              @click="onSelectKey(item)"
            >
              <span class="table-icon-spacer" />
              <component :is="typeIcon(item.type)" class="table-icon" :size="lucideSize('0.875rem')" />
              <span class="table-name">{{ item.name }}</span>
            </div>
          </RecycleScroller>
        </div>

        <!-- Pagination -->
        <div class="redis-pagination">
          <span class="result-count">{{ t('redis.scanCount', { n: keys.length }) }}</span>
          <span style="flex:1"></span>
          <el-select v-model="pageSize" size="small" style="width: 4.375rem" @change="onPageSizeChange">
            <el-option v-for="s in pageSizes" :key="s" :label="String(s)" :value="s" />
          </el-select>
          <button class="page-btn" :disabled="cursorStack.length === 0" @click="onPrevPage"><ChevronLeft :size="lucideSize('0.875rem')" /></button>
          <span class="page-num">{{ currentPage }}</span>
          <button class="page-btn" :disabled="nextCursor === 0 && !hasMore" @click="onNextPage"><ChevronRight :size="lucideSize('0.875rem')" /></button>
        </div>
      </div>

      <!-- Resizer -->
      <div v-if="!isNarrowScreen && treeOpen" class="redis-resizer" @mousedown="onResizeStart" />

      <!-- Right Panel: Value Editor -->
      <div class="redis-right">
        <template v-if="selectedKey">
          <div class="key-meta">
            <button
              class="btn btn-ghost btn-icon btn-sm m-tree-toggle"
              :title="treeOpen ? t('common.collapse') : t('common.expand')"
              @click="treeOpen = !treeOpen"
            >
              <PanelLeft :size="lucideSize('0.875rem')" />
            </button>
            <div class="meta-row"><span class="meta-label">{{ t('redis.keyName') }}</span><span class="meta-value">{{ selectedKey }}</span></div>
            <div class="meta-row"><span class="meta-label">{{ t('redis.type') }}</span><span class="meta-value">{{ selectedKeyInfo?.type || '-' }}</span></div>
            <div class="meta-row">
              <span class="meta-label">{{ t('redis.ttl') }}</span>
              <span class="meta-value ttl-row">
                <el-input-number v-model="editTTL" size="small" style="width: 6.25rem" :min="-1" controls-position="right" />
                <el-button size="small" @click="onSetTTL">{{ t('redis.setTTL') }}</el-button>
                <span style="font-size:0.6875rem;color:var(--text-muted)">{{ t('redis.ttlHint') }}</span>
              </span>
            </div>
          </div>

          <div class="redis-right-content" v-loading="keyLoading">
            <div v-if="valueTruncated" class="truncate-hint">
              {{ t('redis.valueTruncatedHint', { loaded: REDIS_VALUE_READ_CAP, total: selectedKeyInfo?.length ?? 0 }) }}
            </div>
            <!-- String Editor -->
            <template v-if="selectedKeyInfo?.type === 'string'">
              <el-input v-model="stringValue" type="textarea" rows="10" :disabled="valueTruncated" />
            </template>

            <!-- Hash Editor -->
            <template v-else-if="selectedKeyInfo?.type === 'hash'">
              <el-table :data="hashEntries" border size="small">
                <el-table-column type="index" label="#" :width="uiPx(40)" />
                <el-table-column prop="field" :label="t('redis.field')">
                  <template #default="{ $index }">
                    <el-input v-model="hashEntries[$index].field" size="small" type="textarea" :rows="1" autosize :disabled="valueTruncated" />
                  </template>
                </el-table-column>
                <el-table-column prop="value" :label="t('redis.value')">
                  <template #default="{ $index }">
                    <el-input v-model="hashEntries[$index].value" size="small" type="textarea" :rows="1" autosize :disabled="valueTruncated" />
                  </template>
                </el-table-column>
                <el-table-column :width="uiPx(50)">
                  <template #default="{ $index }">
                    <button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" :disabled="valueTruncated" @click="hashEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button>
                  </template>
                </el-table-column>
              </el-table>
              <el-button size="small" style="margin-top: 0.25rem" :disabled="valueTruncated" @click="hashEntries.push({ field: '', value: '' })"><Plus :size="lucideSize('0.875rem')" /></el-button>
            </template>

            <!-- List Editor -->
            <template v-else-if="selectedKeyInfo?.type === 'list'">
              <el-table :data="listEntries" border size="small">
                <el-table-column :width="uiPx(26)" class-name="drag-col">
                  <template #default="{ $index }">
                    <span class="drag-handle" draggable="true" @dragstart="onListDragStart($index)" @dragover.prevent="onListDragOver($index)" @drop="onListDrop($index)"><GripVertical :size="lucideSize('0.875rem')" /></span>
                  </template>
                </el-table-column>
                <el-table-column type="index" label="#" :width="uiPx(40)" />
                <el-table-column :label="t('redis.value')">
                  <template #default="{ $index }">
                    <el-input v-model="listEntries[$index]" size="small" type="textarea" :rows="1" autosize :disabled="valueTruncated" />
                  </template>
                </el-table-column>
                <el-table-column :width="uiPx(50)">
                  <template #default="{ $index }">
                    <button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" :disabled="valueTruncated" @click="listEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button>
                  </template>
                </el-table-column>
              </el-table>
              <el-button size="small" style="margin-top: 0.25rem" :disabled="valueTruncated" @click="addListItem"><Plus :size="lucideSize('0.875rem')" /></el-button>
            </template>

            <!-- Set Editor -->
            <template v-else-if="selectedKeyInfo?.type === 'set'">
              <el-table :data="setEntries" border size="small">
                <el-table-column type="index" label="#" :width="uiPx(40)" />
                <el-table-column :label="t('redis.member')">
                  <template #default="{ $index }">
                    <el-input v-model="setEntries[$index]" size="small" type="textarea" :rows="1" autosize :disabled="valueTruncated" />
                  </template>
                </el-table-column>
                <el-table-column :width="uiPx(50)">
                  <template #default="{ $index }">
                    <button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" :disabled="valueTruncated" @click="setEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button>
                  </template>
                </el-table-column>
              </el-table>
              <el-button size="small" style="margin-top: 0.25rem" :disabled="valueTruncated" @click="setEntries.push('')"><Plus :size="lucideSize('0.875rem')" /></el-button>
            </template>

            <!-- ZSet Editor -->
            <template v-else-if="selectedKeyInfo?.type === 'zset'">
              <el-table :data="zsetEntries" border size="small">
                <el-table-column type="index" label="#" :width="uiPx(40)" />
                <el-table-column :label="t('redis.member')">
                  <template #default="{ $index }">
                    <el-input v-model="zsetEntries[$index].member" size="small" type="textarea" :rows="1" autosize :disabled="valueTruncated" />
                  </template>
                </el-table-column>
                <el-table-column :label="t('redis.score')" :width="uiPx(100)">
                  <template #default="{ $index }">
                    <el-input-number v-model="zsetEntries[$index].score" size="small" controls-position="right" style="width: 100%" :disabled="valueTruncated" />
                  </template>
                </el-table-column>
                <el-table-column :width="uiPx(50)">
                  <template #default="{ $index }">
                    <button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" :disabled="valueTruncated" @click="zsetEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button>
                  </template>
                </el-table-column>
              </el-table>
              <el-button size="small" style="margin-top: 0.25rem" :disabled="valueTruncated" @click="zsetEntries.push({ member: '', score: 0 })"><Plus :size="lucideSize('0.875rem')" /></el-button>
            </template>

            <!-- Action buttons -->
            <div class="value-actions">
              <el-button type="primary" size="small" @click="onSave" :loading="saving" :disabled="valueTruncated">{{ t('redis.save') }}</el-button>
              <el-button size="small" @click="onRevert">{{ t('redis.revert') }}</el-button>
              <el-button type="danger" size="small" @click="onDeleteKey" :loading="deleting">{{ t('redis.deleteKey') }}</el-button>
            </div>
          </div>
        </template>
        <div v-else class="redis-placeholder full">
          <button
            class="btn btn-ghost btn-icon btn-sm m-tree-toggle"
            :title="treeOpen ? t('common.collapse') : t('common.expand')"
            @click="treeOpen = !treeOpen"
          >
            <PanelLeft :size="lucideSize('0.875rem')" />
          </button>
          {{ t('redis.selectKey') }}
        </div>
      </div>
    </div>

    <!-- New Key Dialog -->
    <el-dialog append-to-body v-model="showNewKeyDialog" :title="t('redis.newKey')" width="31.25rem">
      <el-form label-width="5rem">
        <el-form-item :label="t('redis.keyName')">
          <el-input v-model="newKeyName" />
        </el-form-item>
        <el-form-item :label="t('redis.ttl')" v-if="newKeyType">
          <el-input-number v-model="newKeyTTL" :min="-1" controls-position="right" style="width: 7.5rem" />
          <span style="font-size:0.6875rem;color:var(--text-muted);margin-left:0.5rem">{{ t('redis.ttlHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('redis.type')">
          <el-radio-group v-model="newKeyType" @change="onNewKeyTypeChange">
            <el-radio-button value="string">String</el-radio-button>
            <el-radio-button value="hash">Hash</el-radio-button>
            <el-radio-button value="list">List</el-radio-button>
            <el-radio-button value="set">Set</el-radio-button>
            <el-radio-button value="zset">ZSet</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <!-- String: value -->
        <el-form-item v-if="newKeyType === 'string'" :label="t('redis.entryValue')">
          <el-input v-model="newStringValue" type="textarea" rows="10" />
        </el-form-item>
        <!-- Hash: field/value table -->
        <template v-if="newKeyType === 'hash'">
          <el-form-item :label="t('redis.entryValue')">
            <el-table :data="newHashEntries" border size="small">
              <el-table-column prop="field" :label="t('redis.field')">
                <template #default="{ $index }"><el-input v-model="newHashEntries[$index].field" size="small" type="textarea" :rows="1" autosize /></template>
              </el-table-column>
              <el-table-column prop="value" :label="t('redis.value')">
                <template #default="{ $index }"><el-input v-model="newHashEntries[$index].value" size="small" type="textarea" :rows="1" autosize /></template>
              </el-table-column>
              <el-table-column :width="uiPx(50)">
                <template #default="{ $index }"><button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" @click="newHashEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button></template>
              </el-table-column>
            </el-table>
            <el-button size="small" style="margin-top: 0.25rem" @click="newHashEntries.push({ field: '', value: '' })"><Plus :size="lucideSize('0.875rem')" /></el-button>
          </el-form-item>
        </template>
        <!-- List: value table -->
        <template v-if="newKeyType === 'list'">
          <el-form-item :label="t('redis.entryValue')">
            <el-table :data="newListEntries" border size="small">
              <el-table-column :width="uiPx(26)" class-name="drag-col">
                <template #default="{ $index }">
                  <span class="drag-handle" draggable="true" @dragstart="onNewListDragStart($index)" @dragover.prevent="onNewListDragOver($index)" @drop="onNewListDrop($index)"><GripVertical :size="lucideSize('0.875rem')" /></span>
                </template>
              </el-table-column>
              <el-table-column type="index" label="#" :width="uiPx(40)" />
              <el-table-column :label="t('redis.value')">
                <template #default="{ $index }"><el-input v-model="newListEntries[$index]" size="small" type="textarea" :rows="1" autosize /></template>
              </el-table-column>
              <el-table-column :width="uiPx(50)">
                <template #default="{ $index }"><button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" @click="newListEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button></template>
              </el-table-column>
            </el-table>
            <el-button size="small" style="margin-top: 0.25rem" @click="newListEntries.push('')"><Plus :size="lucideSize('0.875rem')" /></el-button>
          </el-form-item>
        </template>
        <!-- Set: member table -->
        <template v-if="newKeyType === 'set'">
          <el-form-item :label="t('redis.entryValue')">
            <el-table :data="newSetEntries" border size="small">
              <el-table-column type="index" label="#" :width="uiPx(40)" />
              <el-table-column :label="t('redis.member')">
                <template #default="{ $index }"><el-input v-model="newSetEntries[$index]" size="small" type="textarea" :rows="1" autosize /></template>
              </el-table-column>
              <el-table-column :width="uiPx(50)">
                <template #default="{ $index }"><button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" @click="newSetEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button></template>
              </el-table-column>
            </el-table>
            <el-button size="small" style="margin-top: 0.25rem" @click="newSetEntries.push('')"><Plus :size="lucideSize('0.875rem')" /></el-button>
          </el-form-item>
        </template>
        <!-- ZSet: member/score table -->
        <template v-if="newKeyType === 'zset'">
          <el-form-item :label="t('redis.entryValue')">
            <el-table :data="newZSetEntries" border size="small">
              <el-table-column prop="member" :label="t('redis.member')">
                <template #default="{ $index }"><el-input v-model="newZSetEntries[$index].member" size="small" type="textarea" :rows="1" autosize /></template>
              </el-table-column>
              <el-table-column prop="score" :label="t('redis.score')" :width="uiPx(100)">
                <template #default="{ $index }"><el-input-number v-model="newZSetEntries[$index].score" size="small" controls-position="right" style="width: 100%" /></template>
              </el-table-column>
              <el-table-column :width="uiPx(50)">
                <template #default="{ $index }"><button class="btn btn-ghost btn-icon btn-sm danger" title="Delete" @click="newZSetEntries.splice($index, 1)"><Trash2 :size="lucideSize('0.875rem')" /></button></template>
              </el-table-column>
            </el-table>
            <el-button size="small" style="margin-top: 0.25rem" @click="newZSetEntries.push({ member: '', score: 0 })"><Plus :size="lucideSize('0.875rem')" /></el-button>
          </el-form-item>
        </template>
      </el-form>
      <template #footer>
        <el-button @click="showNewKeyDialog = false">{{ t('redis.cancel') }}</el-button>
        <el-button type="primary" @click="onCreateKey">{{ t('redis.create') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { lucideSize } from '../utils/lucideSize'
import { ref, watch, computed, onUnmounted } from 'vue'
import { ElMessageBox } from 'element-plus'
import { msg } from '../services/message'
import { Trash2, Plus, GripVertical, RefreshCw, ChevronLeft, ChevronRight, ChevronDown, Folder, FolderTree, List, Type, TableProperties, ListMinus, ListOrdered, PanelLeft } from '@lucide/vue'
import { isNarrowScreen } from '../utils/platform'
import { useI18n } from '../i18n'
import { uiPx } from '../utils/uiScale'
import {
  RedisScanKeys,
  RedisGetKeyInfo,
  RedisGetString,
  RedisSetString,
  RedisGetHashAll,
  RedisHashSetFields,
  RedisHashDel,
  RedisGetHashFields,
  RedisGetListRange,
  RedisListPush,
  RedisReplaceList,
  RedisGetSetAll,
  RedisGetSetMembers,
  RedisSetAdd,
  RedisSetRemove,
  RedisGetSortedSetRange,
  RedisGetZSetRangeCapped,
  RedisZSetAdd,
  RedisZSetRemove,
  RedisDeleteKey,
  RedisSetKeyTTL,
  RedisSwitchDB,
  RedisKeyspaceInfo,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import { diffHashFields, diffMembers, diffScoredMembers, isValueTruncated, REDIS_VALUE_READ_CAP } from '../services/redisSave'
import type { RedisKeyInfo, FieldEntry, ScoredMember, ScanResult, KeyNode } from '../types/redis'

const props = defineProps<{ sessionId: string; keySeparator?: string }>()
const { t } = useI18n()

// Separator for tree grouping; defaults to ":" when empty — the namespace
// convention shared by every Redis GUI. Grouping only reflects the currently
// scanned page (SCAN is random-order), so folder contents are a lower bound.
const separator = computed(() => (props.keySeparator ?? '').length === 1 ? props.keySeparator! : ':')
const treeMode = ref(true)
const keyTree = computed(() => treeMode.value ? buildKeyTree(keys.value, separator.value) : [])

// ── Key tree building ──
// Nodes are built from the currently scanned page of keys by splitting names
// on the separator. Folder counts are recursive sums over loaded keys only —
// the same approximation every Redis GUI makes.

function buildKeyTree(keys: RedisKeyInfo[], sep: string): KeyNode[] {
  interface RawNode { children: Map<string, RawNode>; key?: RedisKeyInfo }
  const root: RawNode = { children: new Map(), key: undefined }

  for (const info of keys) {
    const parts = info.name.split(sep)
    let cur = root
    for (let i = 0; i < parts.length; i++) {
      if (i === parts.length - 1) {
        // Leaf slot keyed by the last segment (unique within its folder); the
        // marker prefix keeps it distinct from a same-named folder slot —
        // keys "app" and "app:cache" really do coexist in Redis.
        cur.children.set('\x00' + parts[i], { children: new Map(), key: info })
      } else {
        let next = cur.children.get(parts[i])
        if (!next) {
          next = { children: new Map(), key: undefined }
          cur.children.set(parts[i], next)
        }
        cur = next
      }
    }
  }

  // Convert nested maps to display nodes.
  const toNodes = (raw: RawNode, path: string): KeyNode[] => {
    const nodes: KeyNode[] = []
    for (const [seg, child] of raw.children) {
      if (seg.startsWith('\x00')) {
        const info = child.key!
        nodes.push({ id: info.name, label: seg.slice(1), count: 1, children: [], keyName: info.name, keyType: info.type })
        continue
      }
      const childPath = path ? path + sep + seg : seg
      const grandchildren = toNodes(child, childPath)
      nodes.push({
        id: childPath,
        label: seg,
        count: grandchildren.reduce((a, n) => a + n.count, 0),
        children: grandchildren,
      })
    }
    return nodes.sort((a, b) => {
      // Folders before keys, then alphabetical within each group. A folder is
      // "has children", so compare that explicitly — comparing children.length
      // directly would order two folders by descendant count instead.
      const af = a.children.length > 0
      const bf = b.children.length > 0
      if (af !== bf) return af ? -1 : 1
      return a.label.localeCompare(b.label)
    })
  }
  return toNodes(root, '')
}

const expandedFolders = ref(new Set<string>())

function onToggleFolder(id: string) {
  const next = new Set(expandedFolders.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedFolders.value = next
}

function onSelectTreeKey(node: KeyNode) {
  closeTree()
  if (node.keyName) {
    onSelectKey({ name: node.keyName, type: node.keyType || 'string', ttl: -1 } as RedisKeyInfo)
  }
}

// DB-style rendering: flatten the nested keyTree to the rows now on screen
// (folders expanded, depth-first), each tagged with its tree depth. The
// template then v-for's over these rows directly — which is exactly how the
// DB tree renders — so the scoped row styles reach every row. Indentation is
// a per-depth left pad on the row itself.
interface VisibleRow {
  id: string
  node: KeyNode
  depth: number
}
const visibleTreeRows = computed<VisibleRow[]>(() => {
  const rows: VisibleRow[] = []
  const walk = (nodes: KeyNode[], depth: number) => {
    for (const n of nodes) {
      rows.push({ id: n.id, node: n, depth })
      if (n.children.length > 0 && expandedFolders.value.has(n.id)) {
        walk(n.children, depth + 1)
      }
    }
  }
  walk(keyTree.value, 0)
  return rows
})

// P1: 虚拟滚动 — RecycleScroller 的数据源与 key 字段，树形/扁平模式统一切换
const listItems = computed<any[]>(() => treeMode.value ? visibleTreeRows.value : keys.value)
const listKeyField = computed(() => treeMode.value ? 'id' : 'name')

// Redis key type → icon, mirroring how the DB tree distinguishes tables/views
// by icon. Shares the .table-icon slot so tree and flat rows stay identical.
function typeIcon(type?: RedisKeyInfo['type']) {
  switch (type) {
    case 'hash': return TableProperties
    case 'list': return List
    case 'set': return ListMinus
    case 'zset': return ListOrdered
    case 'string':
    default: return Type
  }
}

// --- Resize ---
const leftWidth = ref(280)

// Narrow screens turn the key tree into an overlay drawer (closed by
// default); wide screens keep it inline and the toggle collapses it.
const treeOpen = ref(!isNarrowScreen.value)
function closeTree() {
  if (isNarrowScreen.value) treeOpen.value = false
}
const resizing = ref(false)
let resizeStartX = 0
let resizeStartWidth = 0

function onResizeStart(e: MouseEvent) {
  resizeStartX = e.clientX
  resizeStartWidth = leftWidth.value
  resizing.value = true
  document.addEventListener('mousemove', onResizeMove)
  document.addEventListener('mouseup', onResizeEnd)
}
function onResizeMove(e: MouseEvent) {
  const dx = e.clientX - resizeStartX
  leftWidth.value = Math.max(180, Math.min(500, resizeStartWidth + dx))
}
function onResizeEnd() {
  resizing.value = false
  document.removeEventListener('mousemove', onResizeMove)
  document.removeEventListener('mouseup', onResizeEnd)
}
onUnmounted(() => {
  if (resizing.value) {
    document.removeEventListener('mousemove', onResizeMove)
    document.removeEventListener('mouseup', onResizeEnd)
  }
})

// --- Key list state ---
const keys = ref<RedisKeyInfo[]>([])
const selectedKey = ref('')
const selectedKeyInfo = ref<RedisKeyInfo | null>(null)
const loading = ref(false)
const scanPattern = ref('*')
const currentDb = ref(0)
const dbSizes = ref<number[]>(new Array(16).fill(-1))

// Pagination
const pageSize = ref(100)
const pageSizes = [50, 100, 200, 500]
const currentPage = ref(1)
const cursorStack = ref<number[]>([])
const nextCursor = ref(0)
const hasMore = ref(true)

// Value editing
const editTTL = ref(-1)
const stringValue = ref('')
const hashEntries = ref<FieldEntry[]>([])
const listEntries = ref<string[]>([])
const setEntries = ref<string[]>([])
const zsetEntries = ref<ScoredMember[]>([])
const keyLoading = ref(false)
const saving = ref(false)
const deleting = ref(false)
// True when the loaded value is only a prefix of the server-side value
// (capped read of a huge key). The editor turns read-only: saving a prefix
// would silently drop everything beyond the cap.
const valueTruncated = ref(false)

// New key dialog
const showNewKeyDialog = ref(false)
const newKeyName = ref('')
const newKeyType = ref('string')
const newKeyTTL = ref(-1)
const newStringValue = ref('')
const newHashEntries = ref<FieldEntry[]>([])
const newListEntries = ref<string[]>([])
const newSetEntries = ref<string[]>([])
const newZSetEntries = ref<ScoredMember[]>([])

function resetNewKeyForm() {
  newKeyName.value = ''
  newKeyType.value = 'string'
  newKeyTTL.value = -1
  newStringValue.value = ''
  newHashEntries.value = [{ field: '', value: '' }]
  newListEntries.value = ['']
  newSetEntries.value = ['']
  newZSetEntries.value = [{ member: '', score: 0 }]
}
function onNewKeyTypeChange() {
  newKeyTTL.value = -1
  newStringValue.value = ''
  newHashEntries.value = [{ field: '', value: '' }]
  newListEntries.value = ['']
  newSetEntries.value = ['']
  newZSetEntries.value = [{ member: '', score: 0 }]
}

// --- Key scanning ---
async function doScan(cursor: number) {
  loading.value = true
  try {
    const result: ScanResult = await RedisScanKeys(props.sessionId, scanPattern.value, cursor, pageSize.value)
    keys.value = (result.keys || []).sort((a, b) => a.name.localeCompare(b.name))
    nextCursor.value = result.cursor
    hasMore.value = result.cursor !== 0
    if (cursor === 0) expandedFolders.value = new Set()
  } catch (e: any) {
    msg.error(`Scan failed: ${e?.message || e}`)
    keys.value = []
  } finally {
    loading.value = false
  }
}
async function onScan() {
  cursorStack.value = []
  currentPage.value = 1
  await doScan(0)
}
async function onNextPage() {
  if (nextCursor.value === 0) return
  cursorStack.value.push(nextCursor.value)
  currentPage.value++
  await doScan(nextCursor.value)
}
async function onPrevPage() {
  const prev = cursorStack.value.pop()
  currentPage.value = Math.max(1, currentPage.value - 1)
  await doScan(prev ?? 0)
}
async function onPageSizeChange() {
  cursorStack.value = []
  currentPage.value = 1
  await doScan(0)
}
async function fetchDBSize() {
  try {
    const info = await RedisKeyspaceInfo(props.sessionId)
    for (let i = 0; i < 16; i++) {
      dbSizes.value[i] = info[i] ?? 0
    }
  } catch (_) { /* ignore */ }
}

// --- DB switching ---
async function onSwitchDB(idx: number) {
  try {
    await RedisSwitchDB(props.sessionId, idx)
    currentDb.value = idx
    cursorStack.value = []
    currentPage.value = 1
    await fetchDBSize()
    await doScan(0)
  } catch (e: any) {
    msg.error(`Switch DB failed: ${e?.message || e}`)
  }
}

// --- Key selection ---
async function onSelectKey(info: RedisKeyInfo) {
  closeTree()
  selectedKey.value = info.name
  keyLoading.value = true
  valueTruncated.value = false
  try {
    const fresh = await RedisGetKeyInfo(props.sessionId, info.name)
    if (fresh) { selectedKeyInfo.value = fresh; editTTL.value = fresh.ttl }
    else { selectedKeyInfo.value = info; editTTL.value = info.ttl }
  } catch (_) { selectedKeyInfo.value = info; editTTL.value = info.ttl }
  try {
    const total = selectedKeyInfo.value?.length
    // Every load is capped at REDIS_VALUE_READ_CAP: a 100k-element key must
    // not fetch (and render) everything. isValueTruncated compares the loaded
    // count against the key's real element count to flag a partial load.
    switch (info.type) {
      case 'string': { const val = await RedisGetString(props.sessionId, info.name); stringValue.value = val || ''; break }
      case 'hash': {
        const entries = await RedisGetHashFields(props.sessionId, info.name, REDIS_VALUE_READ_CAP)
        hashEntries.value = entries || []
        valueTruncated.value = isValueTruncated(hashEntries.value.length, total)
        break
      }
      case 'list': {
        const items = await RedisGetListRange(props.sessionId, info.name, 0, REDIS_VALUE_READ_CAP - 1)
        listEntries.value = items || []
        valueTruncated.value = isValueTruncated(listEntries.value.length, total)
        break
      }
      case 'set': {
        const members = await RedisGetSetMembers(props.sessionId, info.name, REDIS_VALUE_READ_CAP)
        setEntries.value = members || []
        valueTruncated.value = isValueTruncated(setEntries.value.length, total)
        break
      }
      case 'zset': {
        const members = await RedisGetZSetRangeCapped(props.sessionId, info.name, '-inf', '+inf', REDIS_VALUE_READ_CAP)
        zsetEntries.value = members || []
        valueTruncated.value = isValueTruncated(zsetEntries.value.length, total)
        break
      }
    }
  } catch (e: any) { msg.error(`Load key failed: ${e?.message || e}`) }
  finally { keyLoading.value = false }
}

// --- Save ---
async function onSave() {
  if (valueTruncated.value) { msg.error(t('redis.valueTruncatedError')); return }
  saving.value = true
  try {
    const key = selectedKey.value
    const keyType = selectedKeyInfo.value?.type
    switch (keyType) {
      case 'string': await RedisSetString(props.sessionId, key, stringValue.value); break
      case 'hash': {
        const current = await RedisGetHashAll(props.sessionId, key)
        const { toSet, toDelete } = diffHashFields(current || [], hashEntries.value)
        if (toDelete.length > 0) await RedisHashDel(props.sessionId, key, toDelete)
        if (toSet.length > 0) await RedisHashSetFields(props.sessionId, key, toSet)
        break
      }
      case 'list': {
        // One atomic MULTI/DEL/RPUSH/EXEC replaces the list; the old
        // pop-every-element flow could leave it empty if the connection
        // dropped mid-save, and cost one round trip per element.
        await RedisReplaceList(props.sessionId, key, listEntries.value)
        break
      }
      case 'set': {
        const current = await RedisGetSetAll(props.sessionId, key)
        const { toAdd, toRemove } = diffMembers(current || [], setEntries.value)
        if (toRemove.length > 0) await RedisSetRemove(props.sessionId, key, toRemove)
        if (toAdd.length > 0) await RedisSetAdd(props.sessionId, key, toAdd)
        break
      }
      case 'zset': {
        const current = await RedisGetSortedSetRange(props.sessionId, key, '-inf', '+inf')
        const { toAdd, toRemove } = diffScoredMembers(current || [], zsetEntries.value)
        if (toRemove.length > 0) await RedisZSetRemove(props.sessionId, key, toRemove)
        if (toAdd.length > 0) await RedisZSetAdd(props.sessionId, key, toAdd)
        break
      }
    }
    msg.success(t('redis.saved'))
    const info = await RedisGetKeyInfo(props.sessionId, key)
    if (info) { selectedKeyInfo.value = info; editTTL.value = info.ttl }
  } catch (e: any) { msg.error(`Save failed: ${e?.message || e}`) }
  finally { saving.value = false }
}

// --- TTL ---
async function onSetTTL() {
  if (!selectedKey.value) return
  try {
    await RedisSetKeyTTL(props.sessionId, selectedKey.value, editTTL.value)
    msg.success(t('redis.ttlUpdated'))
    const info = await RedisGetKeyInfo(props.sessionId, selectedKey.value)
    if (info) selectedKeyInfo.value = info
  } catch (e: any) { msg.error(`Set TTL failed: ${e?.message || e}`) }
}

// --- Revert ---
async function onRevert() {
  if (!selectedKeyInfo.value) return
  await onSelectKey(selectedKeyInfo.value)
}

// --- Delete key ---
async function onDeleteKey() {
  try {
    await ElMessageBox.confirm(t('redis.confirmDelete', { key: selectedKey.value }), t('redis.delete'), { confirmButtonText: t('redis.delete'), cancelButtonText: t('redis.cancel'), type: 'warning' })
  } catch (_) { return }
  deleting.value = true
  try {
    await RedisDeleteKey(props.sessionId, selectedKey.value)
    msg.success('Key deleted')
    selectedKey.value = ''
    selectedKeyInfo.value = null
    await doScan(cursorStack.value.length > 0 ? cursorStack.value[cursorStack.value.length - 1] : 0)
  } catch (e: any) { msg.error(`Delete failed: ${e?.message || e}`) }
  finally { deleting.value = false }
}

// --- New key ---
function onShowNewKeyDialog() { resetNewKeyForm(); showNewKeyDialog.value = true }
async function onCreateKey() {
  if (!newKeyName.value.trim()) { msg.warning(t('redis.keyNameRequired')); return }
  try {
    switch (newKeyType.value) {
      case 'string': await RedisSetString(props.sessionId, newKeyName.value, newStringValue.value); break
      case 'hash': {
        const entries = newHashEntries.value.filter(e => e.field)
        if (entries.length > 0) await RedisHashSetFields(props.sessionId, newKeyName.value, entries)
        else await RedisHashSetFields(props.sessionId, newKeyName.value, [{ field: 'field', value: '' }])
        break
      }
      case 'list': {
        const items = newListEntries.value.filter(s => s.trim())
        if (items.length > 0) await RedisListPush(props.sessionId, newKeyName.value, 'right', items)
        else await RedisListPush(props.sessionId, newKeyName.value, 'right', [''])
        break
      }
      case 'set': {
        const members = newSetEntries.value.map(s => s.trim()).filter(s => s)
        if (members.length > 0) await RedisSetAdd(props.sessionId, newKeyName.value, members)
        else await RedisSetAdd(props.sessionId, newKeyName.value, ['member'])
        break
      }
      case 'zset': {
        const entries = newZSetEntries.value.filter(e => e.member)
        if (entries.length > 0) await RedisZSetAdd(props.sessionId, newKeyName.value, entries)
        else await RedisZSetAdd(props.sessionId, newKeyName.value, [{ member: 'member', score: 0 }])
        break
      }
    }
    if (newKeyTTL.value >= 0) { await RedisSetKeyTTL(props.sessionId, newKeyName.value, newKeyTTL.value) }
    showNewKeyDialog.value = false
    msg.success(t('redis.keyCreated'))
    await doScan(cursorStack.value.length > 0 ? cursorStack.value[cursorStack.value.length - 1] : 0)
  } catch (e: any) { msg.error(`Create key failed: ${e?.message || e}`) }
}

// --- List drag reorder ---
let dragIdx = -1
function onListDragStart(idx: number) { dragIdx = idx }
function onListDragOver(idx: number) {
  if (dragIdx === -1 || dragIdx === idx) return
  const item = listEntries.value.splice(dragIdx, 1)[0]
  listEntries.value.splice(idx, 0, item)
  dragIdx = idx
}
function onListDrop(_idx: number) { dragIdx = -1 }
function addListItem() { dragIdx = -1; listEntries.value.push('') }

let newDragIdx = -1
function onNewListDragStart(idx: number) { newDragIdx = idx }
function onNewListDragOver(idx: number) {
  if (newDragIdx === -1 || newDragIdx === idx) return
  const item = newListEntries.value.splice(newDragIdx, 1)[0]
  newListEntries.value.splice(idx, 0, item)
  newDragIdx = idx
}
function onNewListDrop(_idx: number) { newDragIdx = -1 }

// --- Init ---
// Re-scan whenever the session id changes: on first mount AND on reconnect
// (a fresh session bound to the panel). Clear the cached keyspace/navigation
// state before loading the new connection's keys.
let lastSessionId: string | null = null
watch(() => props.sessionId, async (newId) => {
  if (!newId || newId === lastSessionId) return
  lastSessionId = newId
  keys.value = []
  cursorStack.value = []
  currentPage.value = 1
  nextCursor.value = 0
  hasMore.value = false
  await fetchDBSize()
  await doScan(0)
})
</script>

<style scoped>
.redis-tab-content {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.redis-main {
  flex: 1;
  display: flex;
  overflow: hidden;
}
.redis-left {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--border-subtle);
  overflow: hidden;
}
.redis-resizer {
  /* Hover area floats over the tree edge (negative margin) so it takes
     no layout width when idle and never blanks the content side. */
  width: 0.25rem;
  margin-left: -0.25rem;
  position: relative;
  z-index: 1;
  cursor: col-resize;
  background: transparent;
  flex-shrink: 0;
  transition: background 0.15s ease;
}
.redis-resizer:hover::after {
  content: '';
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: 0.1875rem;
  background: var(--accent);
  box-shadow: 0 0 0.375rem var(--accent-glow);
}
.redis-right {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.redis-toolbar {
  display: flex;
  gap: 0.25rem;
  align-items: center;
  padding: 0.5rem;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.redis-key-list {
  flex: 1;
  overflow: hidden;
  min-height: 0;
}
/* P1: RecycleScroller 占满键列表容器并自行管理滚动 */
.redis-scroller {
  height: 100%;
}
/* Rows share the DB tree's .table-item/.table-name rule set so key leaves
   match database tables exactly; folders use the shared .db-header classes.
   Flat mode (no nesting) is the same leaf row as tree mode. Indentation: the
   1.875rem spacer (arrow 12 + gap + icon 14 + gap) lines leaf text up with the
   folder label above. */
.table-item {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.375rem 0.5rem;
  user-select: none;
  transition: background 0.12s ease;
}
.table-item:hover { background: var(--bg-hover); }
.table-item.selected { background: var(--bg-hover); }
.table-icon-spacer {
  /* Equal to the folder row's .db-arrow width so a leaf key's icon sits in
     the same column as its folder's arrow/icon — indentation is then purely
     the per-depth pad, and a leaf under a folder is exactly one level deeper
     (DB's table-under-db relationship) instead of two. */
  width: 0.75rem;
  flex-shrink: 0;
}
.table-icon {
  flex-shrink: 0;
  color: var(--text-muted);
}
.table-name {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-header {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.375rem 0.5rem;
  user-select: none;
  transition: background 0.12s ease;
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  color: var(--text-primary);
}
.db-header:hover {
  background: var(--bg-hover);
}
.db-arrow {
  width: 0.75rem;
  flex-shrink: 0;
  color: var(--text-muted);
  display: flex;
  align-items: center;
}
.db-arrow:hover {
  color: var(--text-primary);
}
.db-icon {
  flex-shrink: 0;
  color: var(--text-muted);
}
.db-name {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.redis-pagination {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.375rem 0.5rem;
  border-top: 1px solid var(--border-subtle);
  flex-shrink: 0;
  font-family: var(--font-ui);
  font-size: 0.75rem;
  color: var(--text-secondary);
}
.result-count {
  color: var(--text-muted);
  font-family: var(--font-ui);
  font-size: 0.75rem;
  white-space: nowrap;
}
.page-btn {
  border: 1px solid var(--border-subtle);
  background: var(--bg-base);
  color: var(--text-secondary);
  padding: 0.125rem 0.25rem;
  border-radius: var(--radius-sm);
  font-size: 0.75rem;
  font-family: var(--font-ui);
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.page-btn:hover:not(:disabled) { background: var(--bg-hover); color: var(--text-primary); }
.page-btn:disabled { opacity: 0.4; }
.page-num {
  min-width: 1.25rem;
  text-align: center;
}
.key-meta {
  padding: 0.5rem 0.75rem 0.75rem 2.5rem;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
  font-family: var(--font-ui);
  font-size: 0.75rem;
  position: relative;
}
/* Tree toggle lives at the far left of the key-meta block. */
.key-meta .m-tree-toggle {
  position: absolute;
  top: 0.375rem;
  left: 0.375rem;
  margin-right: 0;
}
.meta-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0;
}
.meta-label {
  color: var(--text-muted);
  min-width: 2rem;
}
.meta-value {
  color: var(--text-primary);
  font-family: var(--font-mono);
  user-select: text;
  word-break: break-all;
  overflow-wrap: break-word;
}
.ttl-row {
  display: flex;
  align-items: center;
  gap: 0.375rem;
}
.redis-right-content {
  flex: 1;
  overflow-y: auto;
  padding: 0.5rem;
}
.value-actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.5rem;
}
.redis-placeholder {
  color: var(--text-secondary);
  font-family: var(--font-ui);
  font-size: 0.875rem;
  padding: 1rem;
}
.redis-placeholder.full {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
}
/* Empty state keeps the toggle reachable at the same top-left spot the
   key-meta block occupies once a key is selected. */
.redis-placeholder .m-tree-toggle {
  position: absolute;
  top: 0.25rem;
  left: 0.25rem;
  margin-right: 0;
}
/* .btn.btn-ghost.btn-icon(.danger) now supplies the base look; keep only
   Redis-specific overrides here (font-size for icon glyphs, table-cell reset). */
.btn-icon.is-disabled,
.btn-icon:disabled { opacity: 0.3; }
.btn-icon:disabled:hover { color: var(--text-secondary); background: none; }
:deep(.el-table .btn-icon) { background: none; }
.drag-handle {
  cursor: grab;
  color: var(--text-muted);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
}
.drag-handle:active { cursor: grabbing; }
.drag-handle:hover { color: var(--text-primary); }
:deep(.drag-col) { overflow: visible !important; text-align: center !important; vertical-align: middle !important; }
:deep(.drag-col .cell) { overflow: visible !important; padding: 0 !important; }

/* Shown when a huge key was loaded capped: the editor is read-only because
   saving the loaded prefix would drop the hidden tail. */
.truncate-hint {
  margin-bottom: 0.375rem;
  padding: 0.25rem 0.5rem;
  font-size: 0.6875rem;
  color: var(--warning);
  background: var(--warning-subtle);
  border: 1px solid var(--warning);
  border-radius: 4px;
}
</style>
