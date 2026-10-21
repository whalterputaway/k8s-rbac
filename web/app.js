'use strict';

const $ = id => document.getElementById(id);
const types = {
  roles: {kind: 'Role', title: 'Roles', description: 'Права доступа к ресурсам внутри namespace.', scoped: true},
  clusterroles: {kind: 'ClusterRole', title: 'ClusterRoles', description: 'Правила доступа на уровне всего кластера.', scoped: false},
  rolebindings: {kind: 'RoleBinding', title: 'RoleBindings', description: 'Назначение роли субъектам в выбранном namespace.', scoped: true},
  clusterrolebindings: {kind: 'ClusterRoleBinding', title: 'ClusterRoleBindings', description: 'Назначение ClusterRole на уровне всего кластера.', scoped: false},
  serviceaccounts: {kind: 'ServiceAccount', title: 'ServiceAccounts', description: 'Учётные записи для приложений и процессов в Kubernetes.', scoped: true}
};
let type = 'roles';
let items = [];
let namespaces = [];
let editing = null;
let config = {};
let adminToken = '';
let generation = 0;
let toastTimer;

function notify(message) {
  $('notification').textContent = message;
  $('notification').hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { $('notification').hidden = true; }, 6000);
}

async function api(path, options = {}) {
  const headers = {'Content-Type': 'application/yaml', ...options.headers};
  if (adminToken) headers.Authorization = `Bearer ${adminToken}`;
  const response = await fetch(path, {...options, headers});
  if (response.status === 401) {
    if (!$('login').open) $('login').showModal();
    throw new Error('Неверный токен администратора');
  }
  if (response.status === 204) return null;
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function fillNamespaces(select, all) {
  const previous = select.value;
  select.replaceChildren();
  if (all) select.add(new Option('Все namespace', ''));
  for (const ns of namespaces) select.add(new Option(ns, ns));
  if ([...select.options].some(option => option.value === previous)) select.value = previous;
  else if (!all && namespaces.includes('default')) select.value = 'default';
}

async function connect() {
  const data = await api('/api/resources/namespaces');
  namespaces = (data.items || []).map(item => item.metadata.name).sort();
  fillNamespaces($('namespace'), true);
  fillNamespaces($('resourceNS'), false);
  await load();
}

async function load() {
  const current = ++generation;
  const currentType = type;
  const query = new URLSearchParams();
  if (types[type].scoped && $('namespace').value) query.set('namespace', $('namespace').value);
  $('count').textContent = 'Загрузка…';
  $('refresh').disabled = true;
  try {
    const data = await api(`/api/resources/${currentType}?${query}`);
    if (current !== generation) return;
    items = (data.items || []).sort((a, b) => a.metadata.name.localeCompare(b.metadata.name));
    render();
    $('updated').textContent = `Обновлено в ${new Date().toLocaleTimeString('ru-RU')}`;
  } catch (error) {
    if (current !== generation) return;
    items = [];
    render();
    $('count').textContent = 'Не удалось загрузить данные';
    notify(error.message);
  } finally {
    if (current === generation) $('refresh').disabled = false;
  }
}

function cell(text) {
  const td = document.createElement('td');
  td.textContent = text;
  td.title = text;
  return td;
}

function render() {
  const filtered = items.filter(item => item.metadata.name.toLowerCase().includes($('search').value.toLowerCase()));
  $('rows').replaceChildren();
  $('count').textContent = `Показано ${filtered.length} из ${items.length}`;
  $('empty').hidden = filtered.length !== 0;
  for (const item of filtered) {
    const row = document.createElement('tr');
    const rules = item.rules || [];
    let summary = rules.map(rule => `${(rule.resources || rule.nonResourceURLs || []).join(', ')}: ${(rule.verbs || []).join(', ')}`).join('; ') || 'Нет правил';
    if (item.roleRef) summary = `${item.roleRef.kind}/${item.roleRef.name} → ${(item.subjects || []).map(subject => subject.name).join(', ') || 'Нет субъектов'}`;
    if (item.kind === 'ServiceAccount') summary = item.automountServiceAccountToken === false ? 'Автомонтирование токена выключено' : 'Сервисный аккаунт';
    row.append(cell(item.metadata.name), cell(item.metadata.namespace || 'Весь кластер'), cell(summary));
    const actions = document.createElement('td');
    const edit = document.createElement('button');
    edit.textContent = 'Открыть';
    edit.addEventListener('click', () => openEditor(item));
    const remove = document.createElement('button');
    remove.textContent = 'Удалить';
    remove.className = 'delete';
    remove.addEventListener('click', () => deleteResource(item));
    actions.append(edit, remove);
    row.append(actions);
    $('rows').append(row);
  }
}

function selectType(next) {
  type = next;
  const spec = types[type];
  $('title').textContent = spec.title;
  $('description').textContent = spec.description;
  $('namespace').disabled = !spec.scoped;
  $('search').value = '';
  document.querySelectorAll('nav button').forEach(button => button.classList.toggle('active', button.dataset.type === type));
  items = [];
  render();
  load();
}

function addRule() {
  const row = document.createElement('div');
  row.className = 'rule';
  row.innerHTML = '<div class="form-grid"><label>API группы (через запятую)<input data-field="groups" placeholder="пусто = core API"></label><label>Ресурсы (через запятую)<input data-field="resources" value="pods" placeholder="pods, deployments"></label><label>Действия (через запятую)<input data-field="verbs" value="get, list, watch"></label><label>Имена ресурсов (необязательно)<input data-field="names" placeholder="my-pod"></label></div><button type="button" class="delete">Удалить правило</button>';
  row.querySelector('button').addEventListener('click', () => row.remove());
  $('rules').append(row);
}

function addSubject() {
  const row = document.createElement('div');
  row.className = 'subject';
  row.innerHTML = '<div class="form-grid"><label>Тип субъекта<select data-field="kind"><option>User</option><option>Group</option><option>ServiceAccount</option></select></label><label>Имя<input data-field="name" placeholder="developer@example.com"></label><label data-label="namespace" hidden>Namespace аккаунта<input data-field="namespace" placeholder="default"></label></div><button type="button" class="delete">Удалить субъект</button>';
  row.querySelector('select').addEventListener('change', event => {
    row.querySelector('[data-label="namespace"]').hidden = event.target.value !== 'ServiceAccount';
  });
  row.querySelector('button').addEventListener('click', () => row.remove());
  $('subjects').append(row);
}

const split = value => value.split(',').map(part => part.trim()).filter(Boolean);
const field = (row, key) => row.querySelector(`[data-field="${key}"]`).value.trim();

function buildManifest() {
  const spec = types[type];
  const obj = {
    apiVersion: type === 'serviceaccounts' ? 'v1' : 'rbac.authorization.k8s.io/v1',
    kind: spec.kind,
    metadata: {name: $('resourceName').value.trim()}
  };
  if (spec.scoped) obj.metadata.namespace = $('resourceNS').value;
  if (type.endsWith('bindings')) {
    obj.roleRef = {apiGroup: 'rbac.authorization.k8s.io', kind: $('roleKind').value, name: $('roleName').value.trim()};
    obj.subjects = [...$('subjects').children].map(row => {
      const kind = field(row, 'kind');
      const subject = {kind, name: field(row, 'name'), apiGroup: kind === 'ServiceAccount' ? '' : 'rbac.authorization.k8s.io'};
      if (kind === 'ServiceAccount') subject.namespace = field(row, 'namespace') || (spec.scoped ? $('resourceNS').value : '');
      return subject;
    });
  } else if (type === 'serviceaccounts') {
    obj.automountServiceAccountToken = false;
  } else {
    obj.rules = [...$('rules').children].map(row => {
      const rule = {apiGroups: split(field(row, 'groups')), resources: split(field(row, 'resources')), verbs: split(field(row, 'verbs'))};
      if (!rule.apiGroups.length) rule.apiGroups = [''];
      const names = split(field(row, 'names'));
      if (names.length) rule.resourceNames = names;
      return rule;
    });
  }
  return obj;
}

function openEditor(item = null) {
  editing = item;
  $('resourceForm').reset();
  $('editorError').textContent = '';
  $('dryResult').textContent = '';
  $('editorTitle').textContent = item ? 'Редактирование ресурса' : 'Новый ресурс';
  $('editorKind').textContent = types[type].kind.toUpperCase();
  $('save').textContent = item ? 'Сохранить' : 'Создать ресурс';
  $('resourceName').value = item ? item.metadata.name : '';
  $('resourceName').disabled = !!item;
  $('resourceNSLabel').hidden = !types[type].scoped;
  fillNamespaces($('resourceNS'), false);
  $('resourceNS').value = item ? item.metadata.namespace || $('resourceNS').value : $('namespace').value || $('resourceNS').value;
  $('resourceNS').disabled = !!item;
  const binding = type.endsWith('bindings');
  $('rulesSection').hidden = binding || type === 'serviceaccounts';
  $('bindingSection').hidden = !binding;
  $('saSection').hidden = type !== 'serviceaccounts';
  $('roleKind').value = type === 'clusterrolebindings' ? 'ClusterRole' : 'Role';
  $('roleKind').disabled = type === 'clusterrolebindings';
  $('rules').replaceChildren();
  $('subjects').replaceChildren();
  addRule();
  addSubject();
  $('advanced').checked = !!item;
  $('advanced').disabled = !!item;
  $('builder').hidden = !!item;
  $('manifestSection').hidden = !item;
  $('resourceName').required = !item;
  $('dryHint').textContent = config.demo ? 'Демо: локальная проверка, без API Kubernetes' : 'Проверьте перед применением';
  if (item) {
    const copy = structuredClone(item);
    delete copy.metadata.managedFields;
    delete copy.status;
    $('manifest').value = JSON.stringify(copy, null, 2);
  }
  $('editor').showModal();
}

async function saveResource(dryRun) {
  $('editorError').textContent = '';
  $('dryResult').textContent = '';
  if (!$('resourceForm').reportValidity()) return;
  const buttons = [$('save'), $('dryRun')];
  buttons.forEach(button => { button.disabled = true; });
  try {
    const query = new URLSearchParams();
    if (types[type].scoped) query.set('namespace', $('resourceNS').value);
    if (dryRun) query.set('dryRun', 'true');
    const suffix = editing ? `/${encodeURIComponent(editing.metadata.name)}` : '';
    const body = $('advanced').checked ? $('manifest').value : JSON.stringify(buildManifest());
    await api(`/api/resources/${type}${suffix}?${query}`, {method: editing ? 'PUT' : 'POST', body});
    if (dryRun) {
      $('dryResult').textContent = config.demo ? 'Локальная проверка пройдена. Полная валидация доступна при подключении к Kubernetes.' : 'Kubernetes принял dry-run. Ресурс не изменён.';
    } else {
      $('editor').close();
      notify(editing ? 'Изменения сохранены' : 'Ресурс создан');
      await load();
    }
  } catch (error) {
    $('editorError').textContent = error.message;
  } finally {
    buttons.forEach(button => { button.disabled = false; });
  }
}

async function deleteResource(item) {
  if (!confirm(`Удалить ${item.kind} «${item.metadata.name}»${item.metadata.namespace ? ` из ${item.metadata.namespace}` : ' из кластера'}? Это может изменить доступ приложений и пользователей.`)) return;
  const query = new URLSearchParams({resourceVersion: item.metadata.resourceVersion});
  if (types[type].scoped) query.set('namespace', item.metadata.namespace);
  try {
    await api(`/api/resources/${type}/${encodeURIComponent(item.metadata.name)}?${query}`, {method: 'DELETE'});
    notify('Ресурс удалён');
    await load();
  } catch (error) { notify(error.message); }
}

$('navigation').addEventListener('click', event => {
  const button = event.target.closest('[data-type]');
  if (button) selectType(button.dataset.type);
});
$('search').addEventListener('input', render);
$('namespace').addEventListener('change', load);
$('refresh').addEventListener('click', load);
$('create').addEventListener('click', () => openEditor());
$('closeEditor').addEventListener('click', () => $('editor').close());
$('addRule').addEventListener('click', addRule);
$('addSubject').addEventListener('click', addSubject);
$('advanced').addEventListener('change', () => {
  if ($('advanced').checked) $('manifest').value = JSON.stringify(buildManifest(), null, 2);
  $('manifestSection').hidden = !$('advanced').checked;
  $('builder').hidden = $('advanced').checked;
  $('resourceName').required = !$('advanced').checked;
});
$('resourceForm').addEventListener('submit', event => { event.preventDefault(); saveResource(false); });
$('resourceForm').addEventListener('input', () => { $('dryResult').textContent = ''; });
$('dryRun').addEventListener('click', () => saveResource(true));
$('login').addEventListener('cancel', event => event.preventDefault());
$('loginForm').addEventListener('submit', async event => {
  event.preventDefault();
  adminToken = $('token').value;
  try {
    await connect();
    $('login').close();
    $('token').value = '';
    $('loginError').textContent = '';
  } catch (error) { $('loginError').textContent = error.message; }
});
$('logout').addEventListener('click', () => { adminToken = ''; items = []; render(); $('login').showModal(); });

(async () => {
  try {
    const response = await fetch('/api/config');
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    config = await response.json();
    $('logout').hidden = !config.authentication;
    if (config.authentication) $('login').showModal();
    else await connect();
  } catch (error) { notify(error.message); }
})();
