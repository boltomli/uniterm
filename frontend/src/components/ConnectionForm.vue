<template>
  <el-dialog append-to-body v-model="visible" :title="isEdit ? t('conn.editTitle') : t('conn.newTitle')" width="700px" class="conn-dialog" @opened="onDialogOpened">
    <div class="conn-layout">
      <!-- Left sidebar: category icons -->
      <div class="conn-categories">
        <div
          v-for="cat in categories"
          :key="cat.key"
          class="cat-item"
          :class="{ active: category === cat.key }"
          @click="onCategorySelect(cat.key)"
        >
          <component :is="cat.icon" :size="20" />
          <span>{{ cat.label }}</span>
        </div>
      </div>

      <!-- Right content: sub-type grid + form -->
      <div class="conn-main">
        <!-- Sub-type icon grid -->
        <div class="subtype-grid">
          <button
            v-for="st in currentSubTypes"
            :key="st.type + (st.dbType || '') + (st.containerRuntime || '')"
            class="subtype-btn"
            :class="{ active: isSubTypeActive(st) }"
            @click="selectType(st)"
          >
            <component :is="st.icon" :size="18" />
            <span>{{ st.label }}</span>
          </button>
        </div>

        <!-- Form fields -->
        <div class="conn-fields">
          <el-form :model="form" label-width="90px" @submit.prevent="onSave">
            <el-form-item :label="t('conn.name')">
              <div class="name-group-row">
                <el-input v-model="form.name" :placeholder="t('conn.namePlaceholder')" class="name-input" />
                <el-tree-select
                  v-model="selectedGroupId"
                  :data="groupTreeData"
                  :render-after-expand="false"
                  check-strictly
                  clearable
                  :placeholder="t('conn.noGroup')"
                  class="group-select"
                />
                <el-button class="new-group-btn" @click="onGroupSelect('__new__')" :title="t('conn.newGroup')">
                  <Plus :size="14" />
                </el-button>
              </div>
            </el-form-item>
            <el-form-item v-if="form.type === 'database' && form.dbType === 'redis'" :label="t('conn.redisMode')">
              <el-radio-group v-model="form.redisMode">
                <el-radio-button label="standalone">{{ t('conn.redisModeStandalone') }}</el-radio-button>
                <el-radio-button label="sentinel">{{ t('conn.redisModeSentinel') }}</el-radio-button>
              </el-radio-group>
            </el-form-item>
            <template v-if="isRedisSentinel">
              <el-form-item :label="t('conn.redisSentinels')" required>
                <el-input v-model="form.redisSentinels" :placeholder="t('conn.redisSentinelsPlaceholder')" />
              </el-form-item>
              <el-form-item :label="t('conn.redisMasterName')" required>
                <el-input v-model="form.redisMasterName" placeholder="mymaster" />
              </el-form-item>
            </template>
            <el-form-item :label="form.type === 's3' ? 'Endpoint' : form.type === 'webdav' ? 'URL' : t('conn.host')" required v-if="form.type !== 'local' && form.type !== 'serial' && form.type !== 'k8s' && form.type !== 'container' && !isRedisSentinel">
              <div class="host-port-row">
                <el-input ref="hostInputRef" v-model="form.host" class="host-input" :placeholder="form.type === 's3' ? 'e.g. https://s3.amazonaws.com' : form.type === 'webdav' ? 'https://dav.example.com/dav/' : t('conn.hostPlaceholder')" />
                <template v-if="form.type !== 's3' && form.type !== 'webdav'">
                  <span class="host-port-sep">:</span>
                  <el-input-number v-model="form.port" :min="0" :max="65535" class="port-input" />
                </template>
              </div>
            </el-form-item>
            <el-form-item v-if="form.type === 'ssh' || form.type === 'mosh' || form.type === 'x11-desktop' || isElasticsearch" :label="t('conn.authType')">
              <el-radio-group v-model="form.authType">
                <el-radio-button label="password">{{ t('conn.password') }}</el-radio-button>
                <el-radio-button v-if="form.type === 'ssh' || form.type === 'mosh' || form.type === 'x11-desktop'" label="key">{{ t('conn.keyPath') }}</el-radio-button>
                <el-radio-button v-if="form.type === 'ssh' || form.type === 'mosh' || form.type === 'x11-desktop'" label="identity">{{ t('conn.identity') }}</el-radio-button>
                <el-radio-button v-if="isElasticsearch" label="apikey">{{ t('conn.esAuthApiKey') }}</el-radio-button>
              </el-radio-group>
            </el-form-item>
            <el-form-item v-if="form.authType !== 'identity' && form.type !== 'vnc' && form.type !== 'spice' && !(form.type === 'database' && form.dbType === 'rqlite') && form.type !== 'local' && form.type !== 'serial' && form.type !== 'tcp' && form.type !== 'k8s' && form.type !== 'container' && !isEsApiKey" :label="form.type === 's3' ? 'Access Key' : t('conn.user')">
              <el-input v-model="form.user" :placeholder="form.type === 's3' ? 'Access Key ID' : t('conn.userPlaceholder')" />
            </el-form-item>
            <el-form-item
              v-if="form.authType === 'identity' && (form.type === 'ssh' || form.type === 'mosh' || form.type === 'x11-desktop')"
              :label="t('conn.identity')"
            >
              <div class="inline-add-row">
                <el-select v-model="form.identityId" filterable style="flex: 1; min-width: 0">
                  <el-option v-for="id in identityStore.identities" :key="id.id" :label="`${id.name} (${id.username})`" :value="id.id" />
                </el-select>
                <el-button class="inline-add-btn" :title="t('conn.newIdentity')" @click="openNewIdentityDialog">
                  <Plus :size="14" />
                </el-button>
              </div>
            </el-form-item>
            <template v-if="form.type === 'rdp' && isWindows">
              <el-form-item :label="t('conn.rdpEnableNLA')">
                <el-select v-model="form.rdpEnableNLA" style="width: 100%">
                  <el-option :value="true" :label="t('conn.rdpEnableNLAOn')" />
                  <el-option :value="false" :label="t('conn.rdpEnableNLAOff')" />
                </el-select>
              </el-form-item>
            </template>
            <el-form-item v-if="form.type !== 'local' && form.type !== 'serial' && form.type !== 'tcp' && form.type !== 'k8s' && form.type !== 'container' && form.authType !== 'identity' && ((form.authType === 'password' && form.type !== 'rdp') || (form.type === 'rdp' && !form.rdpEnableNLA) || form.type === 'vnc' || form.type === 'spice' || form.type === 'database' || form.type === 'telnet' || form.type === 'ftp' || form.type === 'smb' || form.type === 'webdav' || form.type === 's3') && !(form.type === 'database' && form.dbType === 'rqlite')" :label="form.type === 's3' ? 'Secret Key' : (isEsApiKey ? t('conn.esApiKey') : t('conn.password'))">
              <el-input v-model="form.password" type="password" show-password :key="passwordInputKey" :placeholder="form.type === 's3' ? 'Secret Access Key' : (isEsApiKey ? t('conn.esApiKeyPlaceholder') : '')" />
            </el-form-item>
            <template v-if="isElasticsearch">
              <el-form-item :label="t('conn.esUseSsl')">
                <el-switch v-model="form.esUseSsl" />
              </el-form-item>
              <el-form-item v-if="form.esUseSsl" :label="t('conn.esSkipVerify')">
                <div class="nla-row">
                  <el-switch v-model="form.esSkipVerify" />
                  <span class="field-hint">{{ t('conn.esSkipVerifyHint') }}</span>
                </div>
              </el-form-item>
              <el-form-item :label="t('conn.esPathPrefix')">
                <el-input v-model="form.esPathPrefix" placeholder="/es" />
              </el-form-item>
            </template>
            <template v-if="isRedisSentinel">
              <el-form-item :label="t('conn.sentinelUser')">
                <el-input v-model="form.sentinelUser" :placeholder="t('conn.sentinelAuthHint')" />
              </el-form-item>
              <el-form-item :label="t('conn.sentinelPassword')">
                <el-input v-model="form.sentinelPassword" type="password" show-password :key="passwordInputKey" :placeholder="t('conn.sentinelAuthHint')" />
              </el-form-item>
            </template>
            <el-form-item v-if="form.authType === 'key' && (form.type === 'ssh' || form.type === 'mosh' || form.type === 'x11-desktop')" :label="t('conn.keyPath')">
              <el-input v-model="form.keyPath" :placeholder="t('conn.keyPathPlaceholder')">
                <template #append>
                  <el-tooltip :content="t('conn.selectKeyFile')" placement="top">
                    <el-button :aria-label="t('conn.selectKeyFile')" @click="selectKeyFile">
                      <el-icon><FolderOpen :size="16" /></el-icon>
                    </el-button>
                  </el-tooltip>
                </template>
              </el-input>
            </el-form-item>
            <el-form-item v-if="form.authType === 'key' && (form.type === 'ssh' || form.type === 'mosh' || form.type === 'x11-desktop')" :label="t('conn.keyPassphrase')">
              <el-input v-model="form.password" type="password" show-password :key="passwordInputKey" :placeholder="t('conn.keyPassphrasePlaceholder')" />
            </el-form-item>
            <el-form-item v-if="form.type === 'database' && form.dbType !== 'rqlite' && form.dbType !== 'redis' && form.dbType !== 'elasticsearch'" :label="t('db.databases')" :required="form.dbType === 'postgres'">
              <el-input v-model="form.dbName" :placeholder="t('db.databases')" />
            </el-form-item>
            <el-form-item v-if="form.type === 'database' && form.dbType !== 'elasticsearch' && form.dbType !== 'redis'" :label="t('db.params')">
              <el-input v-model="form.dbParams" :placeholder="defaultParamsHint" style="width:100%" />
            </el-form-item>
            <el-form-item v-if="form.type === 'local'" :label="t('conn.shell')">
              <el-select v-model="form.shellPath" filterable>
                <el-option
                  v-for="sh in shellOptions"
                  :key="sh.value"
                  :label="sh.label"
                  :value="sh.value"
                />
              </el-select>
            </el-form-item>
            <template v-if="form.type === 'serial'">
              <el-form-item :label="t('serial.portLabel')" required>
                <div style="display:flex;gap:8px;width:100%">
                  <el-select v-model="form.serialPort" :placeholder="portPlaceholder" :disabled="serialPorts.length === 0 || serialScanning" :loading="serialScanning" style="flex:1">
                    <el-option v-for="p in serialPorts" :key="p" :label="p" :value="p" />
                  </el-select>
                  <el-button :icon="RefreshCw" :loading="serialScanning" @click="scanSerialPorts">
                    {{ t('serial.scan') }}
                  </el-button>
                </div>
              </el-form-item>
              <el-form-item :label="t('serial.baudRate')">
                <el-autocomplete
                  v-model="serialBaudRateInput"
                  :fetch-suggestions="queryBaudRateSuggestions"
                  :placeholder="t('serial.baudRate')"
                  clearable
                  style="width:100%"
                />
              </el-form-item>
              <el-form-item :label="t('serial.dataBits')">
                <el-select v-model="serialDataBitsValue">
                  <el-option v-for="b in [5,6,7,8]" :key="b" :label="String(b)" :value="b" />
                </el-select>
              </el-form-item>
              <el-form-item :label="t('serial.stopBits')">
                <el-select v-model="serialStopBitsValue">
                  <el-option v-for="b in [1,1.5,2]" :key="b" :label="String(b)" :value="b" />
                </el-select>
              </el-form-item>
              <el-form-item :label="t('serial.parity')">
                <el-select v-model="serialParityValue">
                  <el-option :label="t('serial.parityNone')" value="none" />
                  <el-option :label="t('serial.parityOdd')" value="odd" />
                  <el-option :label="t('serial.parityEven')" value="even" />
                  <el-option :label="t('serial.parityMark')" value="mark" />
                  <el-option :label="t('serial.paritySpace')" value="space" />
                </el-select>
              </el-form-item>
            </template>
            <template v-if="form.type === 'smb'">
              <el-form-item label="Domain" required>
                <el-input v-model="form.smbDomain" placeholder="e.g. WORKGROUP" />
              </el-form-item>
              <el-form-item label="Share">
                <el-input v-model="form.smbShare" placeholder="Share name (leave empty to browse all)" />
              </el-form-item>
            </template>
            <template v-if="form.type === 's3'">
              <el-form-item label="Region" required>
                <el-input v-model="form.s3Region" placeholder="us-east-1" />
              </el-form-item>
              <el-form-item label="Bucket">
                <el-input v-model="form.s3Bucket" placeholder="my-bucket (leave empty to list all buckets)" />
              </el-form-item>
              <el-form-item label="URL style">
                <el-select v-model="form.s3UrlStyle">
                  <el-option label="Virtual-hosted (https://bucket.endpoint/key)" value="virtual" />
                  <el-option label="Path (https://endpoint/bucket/key)" value="path" />
                </el-select>
              </el-form-item>
            </template>
            <!-- ── K8s 字段 ── -->
            <template v-if="form.type === 'k8s'">
              <el-form-item :label="t('conn.k8sConfigSource')">
                <el-radio-group v-model="k8sSourceMode">
                  <el-radio-button label="inline">{{ t('conn.k8sConfigSourceInline') }}</el-radio-button>
                  <el-radio-button label="file">{{ t('conn.k8sConfigSourceFile') }}</el-radio-button>
                </el-radio-group>
              </el-form-item>

              <el-form-item v-if="k8sSourceMode === 'file'" :label="t('conn.k8sConfigPath')">
                <el-input v-model="form.k8sConfigPath" placeholder="~/.kube/config">
                  <template #append>
                    <el-button @click="pickKubeconfigFile">
                      <el-icon><FolderOpen :size="16" /></el-icon>
                    </el-button>
                  </template>
                </el-input>
              </el-form-item>

              <el-form-item v-else :label="t('conn.k8sConfigInline')">
                <el-input v-model="form.k8sConfigInline" type="textarea" :rows="6" placeholder="apiVersion: v1..." />
              </el-form-item>

              <el-form-item :label="t('conn.k8sContext')">
                <div style="display: flex; align-items: center; gap: 8px; width: 100%">
                  <el-select v-model="form.k8sContext" filterable :placeholder="k8sContextsError || ''" :loading="k8sContextsLoading" style="flex: 1">
                    <el-option v-for="c in k8sContexts" :key="c.name" :value="c.name" :label="c.current ? c.name + ' (current)' : c.name" />
                  </el-select>
                  <el-button @click="reloadK8sContexts" :loading="k8sContextsLoading" :title="t('conn.k8sReloadContexts')">
                    <el-icon><RefreshCw :size="16" /></el-icon>
                  </el-button>
                </div>
              </el-form-item>

              <el-form-item :label="t('conn.k8sNamespace')">
                <el-select
                  v-model="form.k8sNamespace"
                  filterable
                  allow-create
                  default-first-option
                  clearable
                  placeholder="Namespace"
                  style="width: 100%"
                  @change="onK8sNamespaceInput"
                >
                  <el-option v-for="ns in k8sNamespaceOptions" :key="ns" :value="ns" :label="ns" />
                </el-select>
              </el-form-item>
            </template>
            <!-- ── Container 字段 ── -->
            <template v-if="form.type === 'container'">
              <el-form-item :label="t('conn.containerTransport')">
                <el-radio-group v-model="form.containerTransport">
                  <el-radio-button label="ssh">{{ t('conn.transportSSH') }}</el-radio-button>
                  <el-radio-button label="local">{{ t('conn.transportLocal') }}</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="form.containerTransport === 'ssh'" :label="t('conn.containerSSHRef')" required>
                <el-select
                  v-model="form.containerSSHConnId"
                  :placeholder="t('conn.containerSSHRefPlaceholder')"
                  filterable
                >
                  <el-option
                    v-for="c in sshConnections"
                    :key="c.id"
                    :label="`${c.name} (${c.user}@${c.host}:${c.port})`"
                    :value="c.id"
                  />
                </el-select>
                <div class="field-hint">
                  {{ sshConnections.length ? t('conn.containerSSHRefHint') : t('conn.containerNoSSH') }}
                </div>
              </el-form-item>
              <el-form-item v-if="form.containerTransport === 'local'">
                <div class="field-hint">{{ t('conn.containerLocalHint') }}</div>
              </el-form-item>
            </template>
            <template v-if="form.type === 'rdp' && isWindows">
              <el-form-item :label="t('rdp.resolution')">
                <el-select v-model="rdpResolution" placeholder="1280×720">
                  <el-option
                    v-for="r in rdpResolutions"
                    :key="r.label"
                    :label="r.label"
                    :value="r.label"
                  />
                </el-select>
              </el-form-item>
              <el-form-item :label="t('conn.rdpSmartSizing')">
                <el-switch v-model="form.rdpSmartSizing" />
              </el-form-item>
            </template>
            <template v-if="form.type === 'x11-desktop'">
              <el-form-item :label="t('conn.x11DesktopDE')" required>
                <el-select v-model="form.x11DesktopDesktopType">
                  <el-option label="GNOME" value="gnome" />
                  <el-option label="KDE" value="kde" />
                  <el-option label="XFCE" value="xfce" />
                  <el-option label="MATE" value="mate" />
                  <el-option label="Cinnamon" value="cinnamon" />
                  <el-option label="Openbox" value="openbox" />
                  <el-option :label="t('conn.x11DesktopDECustom')" value="custom" />
                </el-select>
              </el-form-item>
              <el-form-item v-if="form.x11DesktopDesktopType === 'custom'"
                            :label="t('conn.x11DesktopCustomCmd')" required>
                <el-input v-model="form.x11DesktopCustomCmd"
                          :placeholder="t('conn.x11DesktopCustomCmdPlaceholder')" />
                <div class="field-hint">{{ t('conn.x11DesktopCustomCmdHint') }}</div>
              </el-form-item>
            </template>
            <el-form-item :label="t('conn.remark')">
              <el-input v-model="form.remark" type="textarea" :rows="3" />
            </el-form-item>
            <div v-if="showAdvancedToggle" class="advanced-toggle" @click="showAdvanced = !showAdvanced">
              <el-icon class="advanced-arrow" :class="{ expanded: showAdvanced }"><ChevronRight :size="14" /></el-icon>
              <span>{{ t('conn.advanced') }}</span>
            </div>
            <template v-if="showAdvanced">
            <el-form-item v-if="form.type === 'database'" :label="t('db.params')">
              <el-input v-model="form.dbParams" :placeholder="defaultParamsHint" style="width:100%" />
            </el-form-item>
            <el-form-item v-if="form.type === 'ssh' || form.type === 'telnet' || form.type === 'mosh' || form.type === 'local'" :label="t('conn.postLoginScript')">
              <div class="post-login-config">
                <el-radio-group v-model="postLoginMode" size="small">
                  <el-radio-button label="script">{{ t('conn.postLoginModeScript') }}</el-radio-button>
                  <el-radio-button label="expect" :disabled="form.type !== 'ssh'">{{ t('conn.postLoginModeExpect') }}</el-radio-button>
                </el-radio-group>
                <el-input
                  v-if="postLoginMode === 'script'"
                  v-model="form.postLoginScript"
                  type="textarea"
                  :rows="3"
                  :placeholder="t('conn.postLoginScriptPlaceholder')"
                />
                <div v-else class="expect-steps">
                  <div class="expect-table">
                    <div class="expect-row expect-head">
                      <span></span>
                      <span>{{ t('conn.expectColExpect') }}</span>
                      <span>{{ t('conn.expectColSend') }}</span>
                      <span>{{ t('conn.expectColTimeout') }}</span>
                      <span>{{ t('conn.expectEnter') }}</span>
                      <span></span>
                    </div>
                    <div
                      v-for="(step, idx) in form.postLoginExpectSteps"
                      :key="idx"
                      class="expect-row"
                    >
                      <span class="step-index">{{ idx + 1 }}</span>
                      <el-input
                        v-model="step.expect"
                        :placeholder="t('conn.expectPlaceholder')"
                        class="expect-input"
                      />
                      <el-input
                        v-model="step.send"
                        :placeholder="t('conn.sendPlaceholder')"
                        class="send-input"
                      />
                      <el-input-number
                        v-model="step.timeoutSecond"
                        :min="1"
                        :max="120"
                        :controls="false"
                        class="timeout-input"
                      />
                      <el-checkbox v-model="step.enter" class="enter-check" />
                      <el-button
                        link
                        type="danger"
                        class="remove-step-btn"
                        :title="t('conn.expectRemoveStep')"
                        @click="removeExpectStep(idx)"
                      >
                        <Trash2 :size="14" />
                      </el-button>
                    </div>
                  </div>
                  <el-button class="add-step-btn" @click="addExpectStep">
                    <Plus :size="14" />
                    {{ t('conn.expectAddStep') }}
                  </el-button>
                  <div class="expect-help">{{ t('conn.expectVariableHint') }}</div>
                </div>
              </div>
            </el-form-item>
            <el-form-item
              v-if="form.type === 'ssh' || form.type === 'telnet' || form.type === 'serial' || form.type === 'mosh' || form.type === 'local' || form.type === 'tcp'"
              :label="t('conn.encoding')"
            >
              <el-select v-model="form.encoding" placeholder="Unicode (UTF-8)">
                <el-option label="Unicode (UTF-8)" value="utf-8" />
                <el-option label="Simplified Chinese (GBK)" value="gbk" />
                <el-option label="Simplified Chinese (GB2312)" value="gb2312" />
                <el-option label="Simplified Chinese (GB18030)" value="gb18030" />
                <el-option label="Traditional Chinese (Big5)" value="big5" />
                <el-option label="Japanese (Shift-JIS)" value="shift-jis" />
                <el-option label="Japanese (EUC-JP)" value="euc-jp" />
                <el-option label="Korean (EUC-KR)" value="euc-kr" />
              </el-select>
            </el-form-item>
            <template v-if="form.type === 'telnet'">
              <el-form-item :label="t('conn.telnetNegotiationMode')">
                <el-select v-model="form.telnetNegotiationMode">
                  <el-option :label="t('conn.telnetNegotiationActive')" value="active" />
                  <el-option :label="t('conn.telnetNegotiationPassive')" value="passive" />
                </el-select>
              </el-form-item>
              <el-form-item :label="t('conn.telnetSendMode')">
                <el-select v-model="form.telnetSendMode">
                  <el-option :label="t('conn.telnetSendModeChar')" value="character" />
                  <el-option :label="t('conn.telnetSendModeLine')" value="line" />
                </el-select>
              </el-form-item>
            </template>
            <template v-if="form.type === 'telnet' || form.type === 'serial' || form.type === 'tcp'">
              <el-form-item :label="t('conn.localEcho')">
                <el-switch v-model="form.localEcho" />
              </el-form-item>
              <el-form-item :label="t('conn.newlineMode')">
                <el-select v-model="form.newlineMode">
                  <el-option label="CR" value="cr" />
                  <el-option label="CR+LF" value="crlf" />
                </el-select>
              </el-form-item>
            </template>
            <el-form-item
              v-if="form.type === 'ssh' || form.type === 'telnet' || form.type === 'serial' || form.type === 'mosh' || form.type === 'tcp'"
              :label="t('conn.backspaceKey')"
            >
              <el-select v-model="form.backspaceKey">
                <el-option label="ASCII Backspace (0x08)" value="bs" />
                <el-option label="ASCII Delete (0x7F)" value="del" />
                <el-option label="VT220 Delete (ESC[3~)" value="vt220" />
              </el-select>
            </el-form-item>
            <el-form-item v-if="form.type === 'ssh'" :label="t('conn.sftpMaxConcurrency')">
              <el-input-number v-model="form.sftpMaxConcurrency" :min="0" :max="20" />
            </el-form-item>
            <el-form-item v-if="form.type === 'ssh'" :label="t('conn.x11Forwarding')">
              <el-switch v-model="form.x11Forwarding" />
              <span v-if="x11HintKey" class="field-hint" style="margin-left: 12px;">{{ t(x11HintKey) }}</span>
            </el-form-item>
            <template v-if="form.type === 'ftp'">
              <el-form-item :label="t('conn.ftpEncryption')">
                <el-select v-model="form.ftpEncryption">
                  <el-option :label="t('conn.ftpEncryptionNone')" value="none" />
                  <el-option :label="t('conn.ftpEncryptionAuto')" value="auto" />
                  <el-option :label="t('conn.ftpEncryptionRequired')" value="required" />
                </el-select>
              </el-form-item>
              <el-form-item v-if="form.ftpEncryption !== 'none'" :label="t('conn.ftpSkipVerify')">
                <el-switch v-model="form.ftpSkipVerify" />
                <div class="field-hint">{{ t('conn.ftpSkipVerifyDesc') }}</div>
              </el-form-item>
              <el-form-item :label="t('conn.ftpPassive')">
                <el-switch v-model="form.ftpPassive" />
              </el-form-item>
              <el-form-item :label="t('conn.ftpEncoding')">
                <el-select v-model="form.ftpEncoding" placeholder="UTF-8">
                  <el-option label="UTF-8" value="utf-8" />
                  <el-option label="GBK" value="gbk" />
                  <el-option label="Shift-JIS" value="shift-jis" />
                  <el-option label="Latin-1" value="latin-1" />
                </el-select>
              </el-form-item>
            </template>
            <template v-if="form.type === 'vnc'">
              <el-form-item :label="t('conn.vncShared')">
                <el-switch v-model="form.vncShared" />
              </el-form-item>
              <el-form-item :label="t('conn.vncRepeaterID')">
                <el-input v-model="form.vncRepeaterID" :placeholder="t('conn.vncRepeaterIDPlaceholder')" />
              </el-form-item>
            </template>
            <el-form-item v-if="showProxy" :label="t('conn.proxy')">
              <div class="inline-add-row">
                <el-select v-model="form.proxyId" :placeholder="t('conn.proxyPlaceholder')" clearable filterable style="flex: 1; min-width: 0">
                  <el-option
                    v-for="p in proxyStore.proxies"
                    :key="p.id"
                    :label="`${p.name} (${p.kind} ${p.host}:${p.port})`"
                    :value="p.id"
                  />
                </el-select>
                <el-button class="inline-add-btn" :title="t('conn.newProxy')" @click="openNewProxyDialog">
                  <Plus :size="14" />
                </el-button>
              </div>
            </el-form-item>
            <el-form-item v-if="showTunnel" :label="t('conn.tunnel')">
              <el-select
                v-model="form.tunnelSSHConnId"
                :placeholder="t('conn.tunnelPlaceholder')"
                clearable
                filterable
              >
                <el-option
                  v-for="c in sshConnections"
                  :key="c.id"
                  :label="`${c.name} (${c.user}@${c.host}:${c.port})`"
                  :value="c.id"
                />
              </el-select>
            </el-form-item>
            <el-form-item
              v-if="['ssh','telnet','serial','mosh','local','tcp'].includes(form.type)"
              :label="t('conn.logOnConnect')"
            >
              <el-switch v-model="form.logOnConnect" />
              <div class="field-hint">{{ t('conn.logOnConnectDesc') }}</div>
            </el-form-item>
            </template>
          </el-form>
        </div>
      </div>
    </div>
    <template #footer>
      <el-button @click="visible = false">{{ t('conn.cancel') }}</el-button>
      <el-button
        v-if="canTest"
        :loading="testStatus === 'checking'"
        :icon="testStatus === 'success' ? CircleCheck : testStatus === 'error' ? CircleX : undefined"
        :class="{ 'test-success': testStatus === 'success', 'test-error': testStatus === 'error' }"
        class="test-status-btn"
        @click="onTest"
      >
        {{ t('conn.testConnection') }}
      </el-button>
      <el-button v-if="!isEdit" @click="onConnectOnly">{{ t('conn.connectOnly') }}</el-button>
      <el-button @click="onSave">{{ t('conn.saveOnly') }}</el-button>
      <el-button type="primary" @click="onConnect">{{ t('conn.saveConnect') }}</el-button>
    </template>
  </el-dialog>

  <!-- New group dialog -->
  <el-dialog append-to-body v-model="showNewGroupDialog" :title="t('conn.newGroupTitle')" width="400px">
    <el-form label-width="80px" @submit.prevent="confirmNewGroup">
      <el-form-item :label="t('conn.groupName')">
        <el-input
          v-model="newGroupName"
          :placeholder="t('conn.groupNamePlaceholder')"
          @keyup.enter="confirmNewGroup"
        />
      </el-form-item>
      <el-form-item :label="t('conn.parentGroup')">
        <el-tree-select
          v-model="newGroupParentId"
          :data="groupTreeData"
          :render-after-expand="false"
          check-strictly
          clearable
          :placeholder="t('conn.noGroup')"
          style="width:100%"
        />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="showNewGroupDialog = false">{{ t('conn.cancel') }}</el-button>
      <el-button type="primary" @click="confirmNewGroup">{{ t('conn.save') }}</el-button>
    </template>
  </el-dialog>

  <!-- New identity / keychain dialog (reuses the settings one) -->
  <IdentityEditDialog v-model:visible="identityDialogVisible" :identity="null" @saved="onIdentitySaved" />
  <!-- New proxy dialog (reuses the settings one) -->
  <ProxyEditDialog v-model:visible="proxyDialogVisible" :proxy="null" @saved="onProxySaved" />
</template>

<script setup lang="ts">
import { reactive, computed, watch, ref, nextTick, onMounted } from 'vue'
import { useConnectionStore } from '../stores/connectionStore'
import { useSettingsStore } from '../stores/settingsStore'
import { useIdentityStore } from '../stores/identityStore'
import { useProxyStore } from '../stores/proxyStore'
import { useI18n } from '../i18n'
import type { ConnectionConfig, PostLoginExpectStep } from '../types/session'
import { OpenFileDialog, GetPlatform, ListSerialPorts, TestConnection } from '../../bindings/github.com/ys-ll/uniterm/app'
import { ElInput } from 'element-plus'
import { msg } from '../services/message'
import { Plus, Trash2, ChevronDown, ChevronRight, FolderOpen, RefreshCw, Terminal, Monitor, Database, DatabaseZap, Layers, DatabaseSearch, SquareTerminal, Zap, Laptop, Cable, FolderUp, HardDrive, Cloud, Globe, MonitorCloud, MonitorSmartphone, Boxes, ShipWheel, AppWindow, ArrowLeftRight, CircleCheck, CircleX } from '@lucide/vue'
import { listContexts } from '../services/k8sClient'
import type { K8sContextInfo } from '../types/k8s'
import IdentityEditDialog from './IdentityEditDialog.vue'
import ProxyEditDialog from './ProxyEditDialog.vue'
import { isSqlDbType } from '../utils/quickConnect'
import type { Identity } from '../types/identity'
import type { Proxy } from '../types/proxy'

const { t } = useI18n()
const connectionStore = useConnectionStore()
const settingsStore = useSettingsStore()
const identityStore = useIdentityStore()
const proxyStore = useProxyStore()

onMounted(() => {
  identityStore.load()
  proxyStore.load()
})

// ── Platform detection (before allSubTypes so it's available in computed closures) ──
const isWindows = ref(/windows/i.test(navigator.userAgent) || /win32/i.test(navigator.platform))
const platform = ref<string>('')
GetPlatform().then(p => { platform.value = p })

// ── Categories & sub-types ──
interface SubTypeInfo {
  type: string
  dbType?: string
  containerRuntime?: string
  label: string
  icon: any
}

const categories = computed(() => [
  { key: 'terminal', label: t('conn.categoryTerminal'), icon: SquareTerminal },
  { key: 'filetransfer', label: t('conn.categoryFileTransfer'), icon: FolderUp },
  { key: 'remote', label: t('conn.categoryRemote'), icon: Monitor },
  { key: 'sql', label: t('db.categorySQL'), icon: Database },
  { key: 'nosql', label: t('db.categoryNoSQL'), icon: DatabaseZap },
  { key: 'container', label: t('conn.categoryContainer'), icon: Boxes },
])

const allSubTypes = computed((): Record<string, SubTypeInfo[]> => ({
  terminal: [
    { type: 'ssh', label: 'SSH (SFTP)', icon: SquareTerminal },
    { type: 'telnet', label: 'Telnet', icon: Terminal },
    { type: 'mosh', label: 'Mosh', icon: Zap },
    { type: 'local', label: t('conn.localTerminal'), icon: Laptop },
    { type: 'serial', label: t('serial.title'), icon: Cable },
    { type: 'tcp', label: 'TCP', icon: ArrowLeftRight },
  ],
  filetransfer: [
    { type: 'ftp', label: 'FTP', icon: FolderUp },
    { type: 'smb', label: 'SMB', icon: HardDrive },
    { type: 's3', label: 'S3', icon: Cloud },
    { type: 'webdav', label: 'WebDAV', icon: Globe },
  ],
  remote: [
    ...(isWindows.value ? [{ type: 'rdp', label: 'RDP', icon: Monitor }] : []),
    { type: 'vnc', label: 'VNC', icon: MonitorSmartphone },
    { type: 'spice', label: 'SPICE', icon: MonitorCloud },
    { type: 'x11-desktop', label: 'X11 Desktop', icon: AppWindow },
  ],
  sql: [
    { type: 'database', dbType: 'mysql', label: 'MySQL', icon: Database },
    { type: 'database', dbType: 'postgres', label: 'PostgreSQL', icon: Database },
    { type: 'database', dbType: 'oracle', label: 'Oracle', icon: Database },
    { type: 'database', dbType: 'sqlserver', label: 'SQL Server', icon: Database },
    { type: 'database', dbType: 'rqlite', label: 'rqlite', icon: Database },
  ],
  nosql: [
    { type: 'database', dbType: 'redis', label: 'Redis', icon: DatabaseZap },
    { type: 'database', dbType: 'mongodb', label: 'MongoDB', icon: Layers },
    { type: 'database', dbType: 'elasticsearch', label: 'Elasticsearch', icon: DatabaseSearch },
  ],
  container: [
    { type: 'k8s', label: 'Kubernetes', icon: ShipWheel },
    { type: 'container', containerRuntime: 'docker', label: 'Docker', icon: Boxes },
    { type: 'container', containerRuntime: 'podman', label: 'Podman', icon: Boxes },
    { type: 'container', containerRuntime: 'nerdctl', label: 'nerdctl', icon: Boxes },
    ...(isWindows.value ? [{ type: 'container', containerRuntime: 'wslc', label: 'WSLC', icon: Boxes }] : []),
  ],
}))

const currentSubTypes = computed(() => allSubTypes.value[category.value] || allSubTypes.value.terminal)

function isSubTypeActive(st: SubTypeInfo): boolean {
  if (st.dbType) {
    return form.type === 'database' && form.dbType === st.dbType
  }
  if (st.containerRuntime) {
    return form.type === 'container' && form.containerRuntime === st.containerRuntime
  }
  return form.type === st.type
}

function selectType(st: SubTypeInfo) {
  if (st.dbType) {
    form.type = 'database'
    form.dbType = st.dbType
  } else if (st.containerRuntime) {
    form.type = 'container'
    form.containerRuntime = st.containerRuntime as 'docker' | 'podman' | 'nerdctl' | 'wslc'
  } else {
    form.type = st.type
  }
}

function onCategorySelect(catKey: string) {
  if (category.value === catKey) return
  const subs = allSubTypes.value[catKey]
  if (subs && subs.length > 0) {
    selectType(subs[0])
  }
}

function getShellLabel(path: string): string {
  if (!path) return ''
  const lower = path.toLowerCase()
  if (lower.startsWith('wsl://')) {
    const distro = path.slice(6)
    return distro ? `WSL - ${distro}` : 'WSL'
  }
  if (lower.includes('pwsh')) return 'PowerShell'
  if (lower.includes('powershell')) return 'Windows PowerShell'
  if (lower.includes('bash')) return 'Git Bash'
  if (lower.includes('cmd')) return 'Command Prompt'
  return path.split(/[\\/]/).pop() || path
}

const shellOptions = computed(() =>
  settingsStore.availableShells.map(sh => ({ label: getShellLabel(sh), value: sh }))
)

const x11HintKey = computed(() => {
  if (platform.value === 'darwin') return 'conn.x11ForwardingDescMac'
  return ''
})
const passwordInputKey = ref(0)

const postLoginMode = ref<'script' | 'expect'>('script')
const showAdvanced = ref(false)

// Connection types that support the "test connection" button (issue #377).
// local/serial/mosh/vnc/rdp/spice/x11-desktop don't have a non-interactive probe.
const TESTABLE_TYPES = ['ssh', 'telnet', 'ftp', 's3', 'webdav', 'smb', 'database', 'k8s', 'container', 'tcp']
// Test-connection result state: 'checking' shows a spinner; 'success'/'error'
// keep a colored status icon on the button until the next test or a reopen.
const testStatus = ref<'idle' | 'checking' | 'success' | 'error'>('idle')
const canTest = computed(() => TESTABLE_TYPES.includes(form.type))

// Serial port config (separate refs so allow-create doesn't produce strings)
const serialPorts = ref<string[]>([])
const serialScanning = ref(false)
const serialBaudRateInput = ref('')
const serialDataBitsValue = ref(8)
const serialStopBitsValue = ref(1)
const serialParityValue = ref('none')

const portPlaceholder = computed(() => {
  if (serialScanning.value) return t('serial.scanning')
  if (serialPorts.value.length === 0) return t('serial.noPorts')
  return t('serial.portLabel')
})

async function scanSerialPorts() {
  serialScanning.value = true
  try {
    serialPorts.value = await ListSerialPorts()
  } catch {
    serialPorts.value = []
  } finally {
    serialScanning.value = false
  }
}

const baudRatePresets = [300, 1200, 2400, 4800, 9600, 14400, 19200, 38400, 57600, 115200, 230400, 460800, 921600]

function queryBaudRateSuggestions(queryString: string, cb: (results: { value: string }[]) => void) {
  const suggestions = baudRatePresets
    .filter(r => String(r).includes(queryString))
    .map(r => ({ value: String(r) }))
  cb(suggestions)
}

const props = defineProps<{
  modelValue: boolean
  editConfig?: ConnectionConfig
  defaultGroupId?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  save: [config: ConnectionConfig]
  connect: [config: ConnectionConfig]
  connectOnly: [config: ConnectionConfig]
}>()

const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v)
})

const hostInputRef = ref<InstanceType<typeof ElInput> | null>(null)

watch(visible, (val) => {
  if (val) {
    testStatus.value = 'idle'
    passwordInputKey.value++
    // Apply group on open: the defaultGroupId watch won't fire when the value
    // is unchanged, so restore it here to avoid losing the group on the second
    // consecutive create within the same group.
    if (!props.editConfig) {
      selectedGroupId.value = props.defaultGroupId
      form.groupId = props.defaultGroupId
    }
  }
})

function onDialogOpened() {
  // New connections: focus the host field by default. The dialog's @opened
  // event fires after the open transition, so the host input is mounted.
  if (!isEdit.value) {
    nextTick(() => hostInputRef.value?.focus())
  }
}

const isEdit = computed(() => !!props.editConfig?.id)

const TERMINAL_TYPES = ['ssh', 'telnet', 'mosh', 'local', 'serial']
const REMOTE_TYPES = ['rdp', 'vnc', 'spice', 'x11-desktop']
const FILETRANSFER_TYPES = ['ftp', 'ssh', 'smb', 'webdav', 's3']

// SQL-family dbTypes fall under the "SQL数据库" top-level category; anything
// else with type 'database' falls under "NoSQL数据库" (see isSqlDbType).
const category = computed(() => {
  if (TERMINAL_TYPES.includes(form.type)) return 'terminal'
  if (FILETRANSFER_TYPES.includes(form.type)) return 'filetransfer'
  if (REMOTE_TYPES.includes(form.type)) return 'remote'
  if (form.type === 'database') return isSqlDbType(form.dbType) ? 'sql' : 'nosql'
  if (form.type === 'k8s' || form.type === 'container') return 'container'
  return 'terminal'
})

const sshConnections = computed(() =>
  connectionStore.connections
    .filter(c => c.type === 'ssh' && c.id !== form.id)
    .sort((a, b) => a.name.localeCompare(b.name))
)

const TUNNEL_UNSUPPORTED = ['spice', 'mosh', 'local', 'serial', 'container']
const showTunnel = computed(() =>
  !TUNNEL_UNSUPPORTED.includes(form.type)
)
const showProxy = computed(() => ['ssh', 'sftp', 'monitor'].includes(form.type))
const showAdvancedToggle = computed(() =>
  showTunnel.value || form.type === 'ssh' || form.type === 'telnet' || form.type === 'mosh' || form.type === 'local' || form.type === 'serial' || form.type === 'ftp'
)

const isRedisSentinel = computed(() =>
  form.type === 'database' && form.dbType === 'redis' && form.redisMode === 'sentinel'
)
const isElasticsearch = computed(() =>
  form.type === 'database' && form.dbType === 'elasticsearch'
)
const isEsApiKey = computed(() =>
  isElasticsearch.value && form.authType === 'apikey'
)

const defaultParamsHint = computed(() => {
  switch (form.dbType) {
    case 'mysql': return '默认: charset=utf8mb4'
    case 'postgres': return '默认: sslmode=disable'
    case 'sqlserver': return '默认: encrypt=disable'
    default: return ''
  }
})

const form = reactive<ConnectionConfig>({
  id: '',
  name: '',
  remark: '',
  type: 'ssh',
  host: '',
  port: 22,
  user: '',
  authType: 'password',
  password: '',
  keyPath: '',
  groupId: undefined,
  rdpFixedWidth: undefined,
  rdpFixedHeight: undefined,
  rdpSmartSizing: true,
  rdpEnableNLA: true,
  dbType: '',
  dbName: '',
  dbParams: '',
  esUseSsl: false,
  esPathPrefix: '',
  esSkipVerify: false,
  redisMode: 'standalone',
  redisMasterName: '',
  redisSentinels: '',
  sentinelUser: '',
  sentinelPassword: '',
  postLoginScript: '',
  postLoginExpectSteps: [],
  sftpMaxConcurrency: 5,
  x11Forwarding: false,
  ftpEncryption: 'none',
  ftpPassive: true,
  ftpEncoding: 'utf-8',
  ftpSkipVerify: false,
  vncShared: true,
  vncRepeaterID: '',
  encoding: 'utf-8',
  backspaceKey: 'bs',
  telnetNegotiationMode: 'active' as 'active' | 'passive',
  telnetLocalEcho: false,
  telnetSendMode: 'character' as 'character' | 'line',
  telnetNewlineMode: 'cr' as 'cr' | 'crlf',
  localEcho: false,
  newlineMode: 'cr' as 'cr' | 'crlf',
  shellPath: '',
  smbDomain: 'WORKGROUP',
  smbShare: '',
  s3Region: 'us-east-1',
  s3Bucket: '',
  s3UrlStyle: 'virtual',
  logOnConnect: false,
  k8sConfigPath: '~/.kube/config',
  k8sConfigInline: '',
  k8sContext: '',
  k8sNamespace: 'default',
  containerTransport: 'ssh',
  containerSSHConnId: undefined,
  containerRuntime: 'docker',
  proxyId: undefined,
  x11DesktopDesktopType: 'gnome',
  x11DesktopCustomCmd: '',
})

const rdpResolutions = [
  { label: t('rdp.fullscreen'), w: -1, h: -1 },
  { label: '800 × 600 (SVGA)', w: 800, h: 600 },
  { label: '1024 × 768 (XGA)', w: 1024, h: 768 },
  { label: '1280 × 720 (HD)', w: 1280, h: 720 },
  { label: '1680 × 1050 (WSXGA+)', w: 1680, h: 1050 },
  { label: '1600 × 1200 (UXGA)', w: 1600, h: 1200 },
  { label: '1920 × 1080 (Full HD)', w: 1920, h: 1080 },
  { label: '2560 × 1440 (QHD)', w: 2560, h: 1440 },
]

const rdpResolution = ref(t('rdp.fullscreen'))

const selectedGroupId = ref<string | undefined>(undefined)

// Tree data for el-tree-select
interface TreeOption {
  value: string
  label: string
  children?: TreeOption[]
}

const groupTreeData = computed<TreeOption[]>(() => {
  function buildTree(nodes: any[]): TreeOption[] {
    return nodes.map(node => ({
      value: node.group.id,
      label: node.group.name,
      children: node.children.length > 0 ? buildTree(node.children) : undefined,
    }))
  }
  return [
    { value: '__none__', label: t('conn.noGroup') },
    ...buildTree(connectionStore.groupedConnections.roots),
  ]
})

const selectedGroupName = computed(() => {
  if (!form.groupId) return ''
  const g = connectionStore.groups.find(g => g.id === form.groupId)
  return g?.name || form.groupId
})

// New group dialog
const showNewGroupDialog = ref(false)
const newGroupName = ref('')
const newGroupParentId = ref<string | undefined>(undefined)

// New identity / proxy dialogs (reuse the settings ones, in "create" mode)
const identityDialogVisible = ref(false)
const proxyDialogVisible = ref(false)

function openNewIdentityDialog() {
  identityDialogVisible.value = true
}

function openNewProxyDialog() {
  proxyDialogVisible.value = true
}

// After creating an item from the "+" button, select it immediately so the
// user keeps typing. The stores are already up to date (the dialogs add via
// the same stores), so no explicit reload is needed.
function onIdentitySaved(id: Identity) {
  form.identityId = id.id
}

function onProxySaved(p: Proxy) {
  form.proxyId = p.id
}

// ── K8s state ──
const k8sSourceMode = ref<'file' | 'inline'>('inline')
const k8sContexts = ref<K8sContextInfo[]>([])
const k8sContextsLoading = ref(false)
const k8sContextsError = ref('')
// Guard against watchers wiping restored state during edit-hydration.
const hydrating = ref(false)

// True once the user hand-edits the namespace field, so that switching the
// k8s context later doesn't silently overwrite a value they typed.
const k8sNamespaceTouched = ref(false)
// Namespace suggestions drawn from the contexts' declared defaults plus the
// current field value. allow-create lets the user type any namespace.
const k8sNamespaceOptions = computed(() => {
  const set = new Set<string>()
  if (form.k8sNamespace) set.add(form.k8sNamespace)
  for (const c of k8sContexts.value) if (c.namespace) set.add(c.namespace)
  return Array.from(set)
})

watch(() => props.editConfig, (config) => {
  if (config) {
    // Always reset first so fields absent from the config (e.g. an empty
    // password) don't leak values from a previously edited connection. Then
    // merge the config over the clean defaults.
    resetForm()
    hydrating.value = true
    Object.assign(form, { ...config, postLoginExpectSteps: cloneExpectSteps(config.postLoginExpectSteps || []) })
    // Backfill legacy ES auth: older versions stored the auth type in a dedicated
    // `esAuthType` ('basic'|'apikey') and the key in `esApiKey`. Both now live in
    // the shared `authType` ('password'|'apikey') and `password` fields.
    if (form.type === 'database' && form.dbType === 'elasticsearch') {
      const legacy = config as any
      const legacyType = legacy?.esAuthType
      if (legacyType) {
        form.authType = legacyType === 'apikey' ? 'apikey' : 'password'
        if (form.authType === 'apikey' && !form.password && legacy?.esApiKey) {
          form.password = legacy.esApiKey
        }
      } else if (form.authType !== 'apikey' && form.authType !== 'password') {
        form.authType = 'password'
      }
    }
    // Existing connections without the field default to NLA off (old behavior).
    form.rdpEnableNLA = config.rdpEnableNLA ?? false
    form.x11Forwarding = config.x11Forwarding ?? false
    postLoginMode.value = (config.postLoginExpectSteps?.length || 0) > 0 ? 'expect' : 'script'
    selectedGroupId.value = config.groupId || undefined
    // Sync serial refs from config
    if (config.serialBaudRate) serialBaudRateInput.value = String(config.serialBaudRate)
    if (config.serialDataBits) serialDataBitsValue.value = config.serialDataBits
    if (config.serialStopBits) serialStopBitsValue.value = config.serialStopBits
    if (config.serialParity) serialParityValue.value = config.serialParity
    if (config.type === 'serial') scanSerialPorts()
    // Sync k8s source-mode radio from restored config
    if (config.type === 'k8s') {
      k8sSourceMode.value = config.k8sConfigInline ? 'inline' : 'file'
    }
    // Defaults for container connections saved before these fields existed
    if (config.type === 'container') {
      form.containerTransport = config.containerTransport ?? 'ssh'
      form.containerRuntime = config.containerRuntime ?? 'docker'
    }
    if (config.type === 'x11-desktop') {
      form.x11DesktopDesktopType = config.x11DesktopDesktopType ?? 'gnome'
      form.x11DesktopCustomCmd = config.x11DesktopCustomCmd ?? ''
    }
    // Sync resolution dropdown to the config's fixed size
    const match = rdpResolutions.find(r => r.w === config.rdpFixedWidth && r.h === config.rdpFixedHeight)
    if (match) rdpResolution.value = match.label
    nextTick(() => { hydrating.value = false })
  } else {
    resetForm()
    if (props.defaultGroupId) {
      selectedGroupId.value = props.defaultGroupId
      form.groupId = props.defaultGroupId
    }
  }
}, { immediate: true })

watch(() => props.defaultGroupId, (gid) => {
  if (!props.editConfig && gid) {
    selectedGroupId.value = gid
    form.groupId = gid
  }
})

// Auto-switch default port when changing type
watch(() => form.type, (newType) => {
  // A result icon for the previous type is stale; clear it.
  testStatus.value = 'idle'
  if (newType !== 'ssh' && postLoginMode.value === 'expect') {
    postLoginMode.value = 'script'
  }
  if (isEdit.value) return
  if (newType === 'ssh') form.port = 22
  else if (newType === 'telnet') form.port = 23
  else if (newType === 'mosh') form.port = 22
  else if (newType === 'x11-desktop') form.port = 22
  else if (newType === 'rdp') form.port = 3389
  else if (newType === 'vnc') form.port = 5900
  else if (newType === 'spice') form.port = 5900
  else if (newType === 'database') form.port = 3306
  else if (newType === 'ftp') form.port = 21
  else if (newType === 'smb') form.port = 445
  else if (newType === 'tcp') form.port = 23
  if (REMOTE_TYPES.includes(newType) || newType === 'database') {
    form.authType = 'password'
  }
  if (newType === 'local' && !form.shellPath && settingsStore.availableShells.length > 0) {
    form.shellPath = settingsStore.availableShells[0]
  }
  if (newType === 'serial') {
    scanSerialPorts()
  }
})

watch(postLoginMode, (mode) => {
  if (mode === 'expect' && (!form.postLoginExpectSteps || form.postLoginExpectSteps.length === 0)) {
    addExpectStep()
  }
})

// Auto-switch default port when changing database type
watch(() => form.dbType, (newType) => {
  if (isEdit.value) return
  if (newType === 'mysql') form.port = 3306
  else if (newType === 'postgres') form.port = 5432
  else if (newType === 'rqlite') form.port = 4001
  else if (newType === 'oracle') form.port = 1521
  else if (newType === 'sqlserver') form.port = 1433
  else if (newType === 'redis') form.port = 6379
  else if (newType === 'mongodb') form.port = 27017
  else if (newType === 'elasticsearch') {
    form.port = 9200
    // ES shares the common `authType` field; default to 'password' unless an API key was already chosen.
    form.authType = form.authType === 'apikey' ? 'apikey' : 'password'
  }
})

// Sync resolution picker to form fields
watch(rdpResolution, (val) => {
  const found = rdpResolutions.find(r => r.label === val)
  if (found) {
    form.rdpFixedWidth = found.w
    form.rdpFixedHeight = found.h
  }
})

// Clear the identity reference when switching away from the identity auth type.
watch(() => form.authType, (val) => {
  if (val !== 'identity') form.identityId = ''
})

function resetForm() {
  form.id = ''
  form.name = ''
  form.remark = ''
  form.type = 'ssh'
  form.host = ''
  form.port = 22
  form.user = ''
  form.authType = 'password'
  form.password = ''
  form.keyPath = ''
  form.identityId = ''
  form.groupId = undefined
  form.rdpFixedWidth = undefined
  form.rdpFixedHeight = undefined
  form.rdpSmartSizing = true
  form.rdpEnableNLA = true
  form.dbType = ''
  form.dbName = ''
  form.dbParams = ''
  form.esUseSsl = false
  form.esPathPrefix = ''
  form.esSkipVerify = false
  form.redisMode = 'standalone'
  form.redisMasterName = ''
  form.redisSentinels = ''
  form.sentinelUser = ''
  form.sentinelPassword = ''
  form.postLoginScript = ''
  form.postLoginExpectSteps = []
  postLoginMode.value = 'script'
  form.sftpMaxConcurrency = 5
  form.ftpEncryption = 'none'
  form.ftpPassive = true
  form.ftpEncoding = 'utf-8'
  form.ftpSkipVerify = false
  form.vncShared = true
  form.vncRepeaterID = ''
  form.encoding = 'utf-8'
  form.backspaceKey = 'bs'
  form.telnetNegotiationMode = 'active'
  form.telnetLocalEcho = false
  form.telnetSendMode = 'character'
  form.telnetNewlineMode = 'cr'
  form.localEcho = false
  form.newlineMode = 'cr'
  form.shellPath = ''
  form.serialPort = ''
  form.serialBaudRate = 115200
  form.serialDataBits = 8
  form.serialStopBits = 1
  form.serialParity = 'none'
  serialBaudRateInput.value = ''
  form.tunnelSSHConnId = undefined
  form.proxyId = undefined
  form.logOnConnect = false
  form.k8sConfigPath = '~/.kube/config'
  form.k8sConfigInline = ''
  form.k8sContext = ''
  form.k8sNamespace = 'default'
  k8sNamespaceTouched.value = false
  form.containerTransport = 'ssh'
  form.containerSSHConnId = undefined
  form.containerRuntime = 'docker'
  k8sSourceMode.value = 'inline'
  k8sContexts.value = []
  k8sContextsLoading.value = false
  k8sContextsError.value = ''
  rdpResolution.value = t('rdp.fullscreen')
  form.x11DesktopDesktopType = 'gnome'
  form.x11DesktopCustomCmd = ''
  selectedGroupId.value = undefined
}

// Sync tree-select value to form
watch(selectedGroupId, (val) => {
  form.groupId = val === '__none__' ? undefined : (val || undefined)
})

function onNodeClick(data: any) {
  // el-tree-select auto-closes and syncs via v-model
}

function onGroupSelect(value: string | undefined) {
  if (value === '__new__') {
    showNewGroupDialog.value = true
    newGroupName.value = ''
    newGroupParentId.value = undefined
    return
  }
}

async function confirmNewGroup() {
  const name = newGroupName.value.trim()
  if (!name) {
    return
  }
  showNewGroupDialog.value = false
  const group = await connectionStore.addGroup(name, newGroupParentId.value)
  newGroupParentId.value = undefined
  newGroupName.value = ''
  form.groupId = group.id
  selectedGroupId.value = group.id
}

async function reloadK8sContexts() {
  k8sContextsLoading.value = true
  k8sContextsError.value = ''
  try {
    const src = k8sSourceMode.value === 'file' ? form.k8sConfigPath : form.k8sConfigInline
    if (!src) return
    const list = await listContexts(src, k8sSourceMode.value === 'file')
    k8sContexts.value = list
    if (!form.k8sContext) {
      const current = list.find(c => c.current)
      if (current) form.k8sContext = current.name
    }
  } catch (e: any) {
    k8sContextsError.value = String(e?.message || e)
  } finally {
    k8sContextsLoading.value = false
  }
}

async function selectKeyFile() {
  try {
    const selected = await OpenFileDialog()
    if (selected) form.keyPath = selected
  } catch (e) {
    console.error('select key file:', e)
  }
}

async function pickKubeconfigFile() {
  try {
    const selected = await OpenFileDialog()
    if (selected) form.k8sConfigPath = selected
  } catch (e) {
    console.error('pick kubeconfig:', e)
  }
}

watch(() => [k8sSourceMode.value, form.k8sConfigPath, form.k8sConfigInline], () => {
  if (hydrating.value) return
  form.k8sContext = ''
  k8sContexts.value = []
})

watch(() => form.type, (t) => {
  if (t === 'k8s' && (form.k8sConfigPath || form.k8sConfigInline)) {
    reloadK8sContexts()
  }
})

function onK8sNamespaceInput() {
  k8sNamespaceTouched.value = true
}

// Adopt the selected context's declared default namespace, unless the user has
// already hand-typed a value. Editing an existing connection runs under
// hydrating, where the stored namespace must win.
watch(() => form.k8sContext, (ctxName) => {
  if (hydrating.value) return
  if (k8sNamespaceTouched.value) return
  if (!ctxName) return
  const ctx = k8sContexts.value.find(c => c.name === ctxName)
  if (ctx && ctx.namespace) form.k8sNamespace = ctx.namespace
})

function generateUniqueName(name: string): string {
  if (!connectionStore.connections.some(c => c.name === name)) {
    return name
  }
  let idx = 1
  while (connectionStore.connections.some(c => c.name === `${name} (${idx})`)) {
    idx++
  }
  return `${name} (${idx})`
}

function normalizeForm(): ConnectionConfig {
  // Sync serial refs into form before normalization
  if (form.type === 'serial') {
    form.serialBaudRate = parseInt(serialBaudRateInput.value, 10) || 115200
    serialBaudRateInput.value = String(form.serialBaudRate)
    form.serialDataBits = serialDataBitsValue.value
    form.serialStopBits = serialStopBitsValue.value
    form.serialParity = serialParityValue.value
  }
  const normalized = { ...form }
  // Identity (密钥库) 的用户名与凭据完全由所引用的 identity 提供，连接时
  // MaterializeIdentity 会以 identity 的 username/password 覆盖本字段。
  // 从别的认证方式切到 identity 时，旧字段若残留 enc:v1: 密文，会被
  // 云同步原样带进仓库（同步规范化只处理 authType=="password" 的连接），
  // 导致他机显示字面量 enc:v1:xxx（issue #711）。保存时清掉两者以绝后患。
  if (normalized.authType === 'identity') {
    normalized.password = ''
    normalized.user = ''
  }
  normalized.postLoginExpectSteps = normalizeExpectSteps(form.postLoginExpectSteps || [])
  if (postLoginMode.value === 'script') {
    normalized.postLoginExpectSteps = []
  } else {
    normalized.postLoginScript = ''
  }
  const redisSentinel = normalized.type === 'database' && normalized.dbType === 'redis' && normalized.redisMode === 'sentinel'
  if (redisSentinel) {
    if (!normalized.redisSentinels?.trim()) throw new Error(t('conn.redisSentinelsRequired'))
    if (!normalized.redisMasterName?.trim()) throw new Error(t('conn.redisMasterNameRequired'))
  } else if (normalized.type !== 'local' && normalized.type !== 'serial' && normalized.type !== 'k8s' && normalized.type !== 'container' && !normalized.host.trim()) {
    throw new Error(t('conn.hostRequired'))
  }
  if (normalized.type === 's3') {
    if (!normalized.user?.trim()) throw new Error('S3: Access Key is required')
    if (!normalized.password?.trim()) throw new Error('S3: Secret Key is required')
  }
  if (normalized.type === 'database' && normalized.dbType === 'postgres' && !normalized.dbName?.trim()) {
    throw new Error(t('db.pgDbNameRequired'))
  }
  if (normalized.type === 'database' && normalized.dbType === 'elasticsearch' && normalized.authType === 'apikey' && !normalized.password?.trim()) {
    throw new Error(t('conn.esApiKeyRequired'))
  }
  if (!normalized.name.trim()) {
    normalized.name = generateUniqueName(
      normalized.type === 'serial' ? (normalized.serialPort || 'Serial')
        : normalized.type === 'k8s' ? (normalized.k8sContext || 'Kubernetes')
        : normalized.type === 'container' ? (normalized.containerRuntime || 'container')
        : redisSentinel ? (normalized.redisMasterName?.trim() || 'redis')
        : normalized.host.trim()
    )
  }
  return normalized
}

function cloneExpectSteps(steps: PostLoginExpectStep[]): PostLoginExpectStep[] {
  return steps.map(step => ({ ...step }))
}

function normalizeExpectSteps(steps: PostLoginExpectStep[]): PostLoginExpectStep[] {
  return steps
    .map(step => ({
      expect: step.expect.trim(),
      send: step.send,
      enter: step.enter !== false,
      timeoutSecond: step.timeoutSecond || 10
    }))
    .filter(step => step.expect || step.send)
}

function addExpectStep() {
  if (!form.postLoginExpectSteps) {
    form.postLoginExpectSteps = []
  }
  form.postLoginExpectSteps.push({
    expect: '',
    send: '',
    enter: true,
    timeoutSecond: 10
  })
}

function removeExpectStep(index: number) {
  form.postLoginExpectSteps?.splice(index, 1)
}

function validateContainer(): boolean {
  if (form.type !== 'container') return true
  if (form.containerTransport === 'ssh' && !form.containerSSHConnId) {
    msg.error(t('conn.containerSSHRefRequired'))
    return false
  }
  if (form.containerTransport === 'ssh' && !sshConnections.value.length) return false
  return true
}

function validateX11Desktop(): boolean {
  if (form.type !== 'x11-desktop') return true
  if (form.x11DesktopDesktopType === 'custom' && !form.x11DesktopCustomCmd?.trim()) {
    msg.error(t('conn.x11DesktopCustomCmdRequired'))
    return false
  }
  return true
}

async function onTest() {
  if (!validateContainer()) return
  if (!validateX11Desktop()) return
  let config: ConnectionConfig
  try {
    config = normalizeForm()
  } catch {
    // Host / required field empty; silently return like onSave/onConnect.
    return
  }
  testStatus.value = 'checking'
  try {
    const desc = await TestConnection(config)
    testStatus.value = 'success'
    msg.success(desc)
  } catch (e: any) {
    testStatus.value = 'error'
    msg.error(String(e?.message || e))
  }
}

function onSave() {
  if (!validateContainer()) return
  if (!validateX11Desktop()) return
  try {
    const config = normalizeForm()
    emit('save', config)
    visible.value = false
    if (!props.editConfig) {
      resetForm()
    }
  } catch (e: any) {
    // Host empty, silently return
  }
}

function onConnectOnly() {
  if (!validateContainer()) return
  if (!validateX11Desktop()) return
  try {
    const config = normalizeForm()
    emit('connectOnly', config)
    visible.value = false
    if (!props.editConfig) {
      resetForm()
    }
  } catch (e: any) {
    // Host empty
  }
}

function onConnect() {
  if (!validateContainer()) return
  if (!validateX11Desktop()) return
  try {
    const config = normalizeForm()
    emit('connect', config)
    visible.value = false
    if (!props.editConfig) {
      resetForm()
    }
  } catch (e: any) {
    // Host empty
  }
}
</script>

<style scoped>
/* Color the connection-test status icon (rendered via el-button's `icon` prop,
   so spacing matches the native loading spinner) by result. */
.test-status-btn.test-success :deep(.el-icon) {
  color: var(--success);
}
.test-status-btn.test-error :deep(.el-icon) {
  color: var(--error);
}

/* ── Layout ── */
.conn-layout {
  display: flex;
  gap: 0;
  min-height: 360px;
}

/* ── Left sidebar ── */
.conn-categories {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 90px;
  flex-shrink: 0;
  padding: 8px 8px 8px 0;
  border-right: 1px solid var(--border-subtle);
}

.cat-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 12px 4px;
  border-radius: var(--radius-md);
  cursor: pointer;
  user-select: none;
  color: var(--text-muted);
  border-left: 2px solid transparent;
  transition: all 0.15s ease;
}

.cat-item:hover {
  background: var(--bg-hover);
  color: var(--text-secondary);
}

.cat-item.active {
  color: var(--accent);
  background: var(--accent-subtle);
  border-left-color: var(--accent);
}

.cat-item span {
  font-size: 11px;
  font-weight: 500;
  font-family: var(--font-ui);
  text-align: center;
  line-height: 1.2;
}

/* ── Right main content ── */
.conn-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  padding: 0 0 0 16px;
}

/* ── Sub-type icon grid ── */
.subtype-grid {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 4px;
  padding-bottom: 14px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--border-subtle);
}

.subtype-btn {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 3px;
  width: 72px;
  height: 52px;
  padding: 4px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-family: var(--font-ui);
  font-size: 11px;
  font-weight: 500;
  transition: all 0.15s ease;
}

.subtype-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
  border-color: var(--border-default);
}

.subtype-btn.active {
  background: linear-gradient(135deg, var(--accent), var(--accent));
  color: var(--on-accent);
  border-color: var(--accent-glow);
  box-shadow: 0 0 0 1px var(--accent-glow), 0 2px 8px var(--accent-glow);
}

.subtype-btn span {
  text-align: center;
  line-height: 1.2;
  font-size: 11px;
  white-space: nowrap;
}

/* ── Form fields ── */
.conn-fields {
  padding-right: 4px;
}

/* ── Name + group row ── */
.name-group-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
}

.name-input {
  flex: 1;
  min-width: 0;
}

.group-select {
  width: 160px;
  flex-shrink: 0;
}

.new-group-btn {
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  padding: 0;
}

/* ── Host + port row ── */
.host-port-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
}

.host-input {
  width: calc(100% - 150px) !important;
}

.host-port-sep {
  color: var(--text-muted);
  font-weight: 500;
}

.port-input {
  width: 130px !important;
  flex-shrink: 0;
}

/* ── Field hint text ── */
.field-hint {
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.4;
}

/* ── Advanced toggle ── */
.advanced-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 10px 0 8px;
  margin-bottom: 4px;
  cursor: pointer;
  user-select: none;
  color: var(--text-secondary);
  font-size: 13px;
  font-weight: 500;
  border-bottom: 1px solid var(--border-subtle);
  transition: color 0.15s;
}

.advanced-toggle:hover {
  color: var(--accent);
}

.advanced-arrow {
  transition: transform 0.2s;
  display: inline-flex;
  align-items: center;
}

.advanced-arrow.expanded {
  transform: rotate(90deg);
}

/* ── Post-login config ── */
.post-login-config {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}

.expect-steps {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.expect-table {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--border-subtle);
  border-radius: 4px;
  overflow: hidden;
}

.expect-row {
  display: grid;
  grid-template-columns: 26px minmax(80px, 1fr) minmax(90px, 1fr) 64px 40px 30px;
  align-items: stretch;
}

.expect-row:not(:last-child) {
  border-bottom: 1px solid var(--border-subtle);
}

.expect-row > * {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 0;
}

.expect-row > *:not(:last-child) {
  border-right: 1px solid var(--border-subtle);
}

.expect-head {
  background: var(--bg-elevated);
  font-size: 12px;
  line-height: 1.2;
  color: var(--text-secondary);
}

.expect-head > span {
  padding: 3px 4px;
}

.expect-row :deep(.el-input__wrapper),
.expect-row :deep(.el-input-number .el-input__wrapper) {
  box-shadow: none;
  border-radius: 0;
}

.expect-input,
.send-input,
.timeout-input {
  width: 100%;
}

.timeout-input :deep(.el-input__inner) {
  text-align: center;
}

.step-index {
  color: var(--text-muted);
  font-size: 12px;
}

.remove-step-btn {
  min-width: 0;
}

.add-step-btn {
  align-self: flex-start;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.expect-help {
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.4;
}

/* ── Group selector row ── */
.group-select-row {
  display: flex;
  gap: 6px;
  align-items: center;
}
.add-group-btn {
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  padding: 0;
}

/* ── Inline select + "+" rows (identity / proxy) ── */
.inline-add-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
}
.inline-add-btn {
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  padding: 0;
}

/* ── Dialog overrides ── */
:deep(.el-dialog__body) {
  padding: 16px 20px;
}
</style>
