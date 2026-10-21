const {test} = require('node:test');
const assert = require('node:assert/strict');
const {buildAccessGraph} = require('../web/graph-model.js');

const role = (kind, name, namespace) => ({kind, metadata: {name, ...(namespace ? {namespace} : {})}, rules: []});
const binding = (name, namespace, roleKind, roleName, subjects) => ({
  kind: namespace ? 'RoleBinding' : 'ClusterRoleBinding', metadata: {name, ...(namespace ? {namespace} : {})},
  roleRef: {kind: roleKind, name: roleName}, subjects
});
const user = name => ({kind: 'User', name});
const group = name => ({kind: 'Group', name});

test('only human assignments and their referenced roles appear by default', () => {
  const data = {
    roles: [role('Role', 'reader', 'app'), role('Role', 'unassigned', 'app')],
    clusterroles: [role('ClusterRole', 'admin')],
    rolebindings: [binding('human', 'app', 'Role', 'reader', [user('alice'), group('developers'), user('system:kube-controller')])],
    clusterrolebindings: [binding('system-only', '', 'ClusterRole', 'admin', [group('system:masters'), {kind: 'ServiceAccount', name: 'controller', namespace: 'kube-system'}])]
  };
  const graph = buildAccessGraph(data);
  assert.deepEqual(graph.nodes.map(node => node.name).sort(), ['alice', 'developers', 'human', 'reader']);
  assert.equal(graph.edges.length, 3);
  assert.equal(graph.chains.length, 2);
});

test('identical role names resolve by kind and namespace', () => {
  const graph = buildAccessGraph({
    roles: [role('Role', 'reader', 'a'), role('Role', 'reader', 'b')],
    clusterroles: [role('ClusterRole', 'reader')],
    rolebindings: [binding('read', 'a', 'Role', 'reader', [user('alice')]), binding('read', 'b', 'Role', 'reader', [user('alice')]), binding('cluster-read', 'a', 'ClusterRole', 'reader', [user('alice')])]
  });
  assert.equal(graph.nodes.filter(node => node.column === 0).length, 1);
  assert.equal(graph.nodes.filter(node => node.column === 2).length, 3);
  assert.ok(graph.nodes.every(node => !node.missing));
  assert.equal(new Set(graph.nodes.map(node => node.id)).size, graph.nodes.length);
});

test('namespace filtering retains cluster-wide assignments', () => {
  const graph = buildAccessGraph({
    rolebindings: [binding('a', 'a', 'Role', 'reader', [user('alice')]), binding('b', 'b', 'Role', 'reader', [user('bob')])],
    clusterrolebindings: [binding('global', '', 'ClusterRole', 'view', [user('carol')])]
  }, {namespace: 'a'});
  assert.deepEqual(graph.nodes.filter(node => node.column === 0).map(node => node.name), ['alice', 'carol']);
});

test('search preserves a complete matching path without unrelated subjects', () => {
  const data = {rolebindings: [binding('team', 'app', 'Role', 'reader', [user('alice'), user('bob')])]};
  const graph = buildAccessGraph(data, {search: 'ALICE'});
  assert.equal(graph.nodes.length, 3);
  assert.equal(graph.edges.length, 2);
  assert.ok(!graph.nodes.some(node => node.name === 'bob'));
  assert.equal(buildAccessGraph(data, {search: 'reader'}).nodes.length, 4);
  assert.equal(buildAccessGraph(data, {search: 'unknown'}).nodes.length, 0);
});

test('missing roles remain visible as unresolved references', () => {
  const graph = buildAccessGraph({rolebindings: [binding('read', 'app', 'Role', 'deleted-role', [user('alice')])]});
  const missing = graph.nodes.find(node => node.column === 2);
  assert.equal(missing.missing, true);
  assert.equal(missing.object, null);
  assert.equal(graph.edges.length, 2);
});

test('optional accounts exclude default and system accounts', () => {
  const graph = buildAccessGraph({rolebindings: [binding('read', 'app', 'Role', 'reader', [
    {kind: 'ServiceAccount', name: 'app-bot'},
    {kind: 'ServiceAccount', name: 'default'},
    {kind: 'ServiceAccount', name: 'controller', namespace: 'kube-system'}
  ])]}, {accounts: true});
  assert.deepEqual(graph.nodes.filter(node => node.column === 0).map(node => [node.name, node.namespace]), [['app-bot', 'app']]);
});

test('identically named users and groups remain separate and input stays unchanged', () => {
  const data = {rolebindings: [binding('read', 'app', 'Role', 'reader', [user('team'), group('team')])]};
  const before = JSON.stringify(data);
  const graph = buildAccessGraph(data);
  assert.equal(graph.nodes.filter(node => node.column === 0).length, 2);
  assert.equal(JSON.stringify(data), before);
  assert.ok(graph.nodes.every(node => Number.isFinite(node.x) && Number.isFinite(node.y)));
});
