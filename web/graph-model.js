'use strict';

function buildAccessGraph(data, options = {}) {
  const nodeMap = new Map();
  const edgeMap = new Map();
  const chains = [];
  const namespace = options.namespace || '';
  const search = (options.search || '').trim().toLowerCase();
  const id = (kind, ns, name) => JSON.stringify([kind, ns || '', name]);
  const roleMap = new Map([...(data.roles || []), ...(data.clusterroles || [])].map(role => [id(role.kind, role.metadata.namespace, role.metadata.name), role]));
  const isHuman = subject => ['User', 'Group'].includes(subject.kind) && !subject.name.startsWith('system:');
  const isAccount = (subject, binding) => options.accounts && subject.kind === 'ServiceAccount' && subject.name !== 'default' && !/^(kube-|kubernetes-)/.test(subject.namespace || binding.metadata.namespace || '') && !subject.name.startsWith('system:');
  for (const binding of [...(data.rolebindings || []), ...(data.clusterrolebindings || [])]) {
    if (namespace && binding.kind === 'RoleBinding' && binding.metadata.namespace !== namespace) continue;
    const ref = binding.roleRef;
    if (!ref) continue;
    const roleID = id(ref.kind, ref.kind === 'Role' ? binding.metadata.namespace : '', ref.name);
    const role = roleMap.get(roleID);
    for (const subject of binding.subjects || []) {
      if (!subject.name || (!isHuman(subject) && !isAccount(subject, binding))) continue;
      const subjectNS = subject.kind === 'ServiceAccount' ? subject.namespace || binding.metadata.namespace : '';
      const searchable = [subject.name, subject.kind, subjectNS, binding.metadata.name, binding.metadata.namespace, ref.name, ref.kind].join(' ').toLowerCase();
      if (search && !searchable.includes(search)) continue;
      const subjectID = id(subject.kind, subjectNS, subject.name);
      const bindingID = id(binding.kind, binding.metadata.namespace, binding.metadata.name);
      nodeMap.set(subjectID, {id: subjectID, column: 0, kind: subject.kind, name: subject.name, namespace: subjectNS || '', object: subject});
      nodeMap.set(bindingID, {id: bindingID, column: 1, kind: binding.kind, name: binding.metadata.name, namespace: binding.metadata.namespace || '', object: binding});
      nodeMap.set(roleID, {id: roleID, column: 2, kind: ref.kind, name: ref.name, namespace: ref.kind === 'Role' ? binding.metadata.namespace : '', object: role || null, missing: !role});
      for (const [from, to] of [[subjectID, bindingID], [bindingID, roleID]]) {
        const edgeID = JSON.stringify([from, to]);
        edgeMap.set(edgeID, {id: edgeID, from, to});
      }
      chains.push({subject: subjectID, binding: bindingID, role: roleID});
    }
  }
  const nodes = [...nodeMap.values()].sort((a, b) => a.column - b.column || a.name.localeCompare(b.name) || a.namespace.localeCompare(b.namespace));
  const columns = [[], [], []];
  for (const node of nodes) columns[node.column].push(node);
  const rows = Math.max(1, ...columns.map(column => column.length));
  for (const column of columns) column.forEach((node, index) => {
    node.x = 40 + node.column * 370;
    node.y = 65 + index * 120 + (rows - column.length) * 60;
  });
  return {nodes, edges: [...edgeMap.values()], chains, width: 1100, height: rows * 120 + 70};
}

if (typeof module !== 'undefined' && module.exports) module.exports = {buildAccessGraph};
