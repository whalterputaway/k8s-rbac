'use strict';

class AccessGraphView {
  constructor(api, openResource) {
    this.api = api;
    this.openResource = openResource;
    this.canvas = document.getElementById('graphCanvas');
    this.world = document.getElementById('graphWorld');
    this.nodesLayer = document.getElementById('graphNodes');
    this.svg = document.getElementById('graphEdges');
    this.data = {};
    this.graph = buildAccessGraph({});
    this.scale = 1;
    this.x = 0;
    this.y = 0;
    this.request = 0;
    this.selected = null;
    this.positions = new Map();
    this.nodeElements = new Map();
    this.edgeElements = new Map();
    this.initialDetails = document.getElementById('graphDetails').cloneNode(true);
    document.getElementById('graphRefresh').addEventListener('click', () => this.load());
    document.getElementById('graphSearch').addEventListener('input', () => this.render());
    document.getElementById('graphNamespace').addEventListener('change', () => this.render());
    document.getElementById('graphAccounts').addEventListener('change', () => this.render());
    document.getElementById('graphZoomIn').addEventListener('click', () => this.zoom(1.2));
    document.getElementById('graphZoomOut').addEventListener('click', () => this.zoom(1 / 1.2));
    document.getElementById('graphFit').addEventListener('click', () => this.fit());
    this.canvas.addEventListener('wheel', event => {
      event.preventDefault();
      const rect = this.canvas.getBoundingClientRect();
      this.zoom(Math.exp(-Math.max(-100, Math.min(100, event.deltaY)) * .004), event.clientX - rect.left, event.clientY - rect.top);
    }, {passive: false});
    this.canvas.addEventListener('pointerdown', event => this.startDrag(event));
    this.canvas.addEventListener('pointermove', event => this.moveDrag(event));
    this.canvas.addEventListener('pointerup', event => this.endDrag(event));
    this.canvas.addEventListener('pointercancel', () => this.cancelDrag());
    this.canvas.addEventListener('lostpointercapture', () => this.cancelDrag());
    this.canvas.addEventListener('keydown', event => {
      if (event.target !== this.canvas) return;
      const offsets = {ArrowLeft: [50, 0], ArrowRight: [-50, 0], ArrowUp: [0, 50], ArrowDown: [0, -50]};
      if (offsets[event.key]) {
        event.preventDefault();
        this.x += offsets[event.key][0];
        this.y += offsets[event.key][1];
        this.transform();
      } else if (event.key === '+' || event.key === '=') { event.preventDefault(); this.zoom(1.2); }
      else if (event.key === '-') { event.preventDefault(); this.zoom(1 / 1.2); }
      else if (event.key === '0') { event.preventDefault(); this.fit(); }
    });
    new ResizeObserver(() => { if (!document.getElementById('graphView').hidden) this.transform(); }).observe(this.canvas);
  }

  async load() {
    const request = ++this.request;
    document.getElementById('graphRefresh').disabled = true;
    document.getElementById('graphCount').textContent = 'Загрузка…';
    try {
      const kinds = ['roles', 'clusterroles', 'rolebindings', 'clusterrolebindings'];
      const result = await Promise.all(kinds.map(kind => this.api('/api/resources/' + kind)));
      if (request !== this.request) return;
      this.data = Object.fromEntries(kinds.map((kind, index) => [kind, result[index].items || []]));
      this.render();
      document.getElementById('graphUpdated').textContent = 'Обновлено в ' + new Date().toLocaleTimeString('ru-RU');
    } catch (error) {
      if (request !== this.request) return;
      this.clear();
      document.getElementById('graphEmpty').hidden = false;
      document.getElementById('graphEmptyText').textContent = error.message;
      document.getElementById('graphCount').textContent = 'Не удалось загрузить связи';
    } finally {
      if (request === this.request) document.getElementById('graphRefresh').disabled = false;
    }
  }

  clear() {
    this.selected = null;
    this.nodesLayer.replaceChildren();
    this.svg.replaceChildren();
    this.nodeElements.clear();
    this.edgeElements.clear();
    document.getElementById('graphDetails').replaceChildren(...[...this.initialDetails.childNodes].map(node => node.cloneNode(true)));
    document.getElementById('graphDetails').hidden = true;
  }

  reset() {
    this.request++;
    this.data = {};
    this.positions.clear();
    this.graph = buildAccessGraph({});
    this.clear();
    document.getElementById('graphCount').textContent = '';
    document.getElementById('graphUpdated').textContent = '';
  }

  render() {
    this.clear();
    this.graph = buildAccessGraph(this.data, {
      namespace: document.getElementById('graphNamespace').value,
      search: document.getElementById('graphSearch').value,
      accounts: document.getElementById('graphAccounts').checked
    });
    const {nodes, edges} = this.graph;
    document.getElementById('graphEmpty').hidden = nodes.length !== 0;
    document.getElementById('graphEmptyText').textContent = document.getElementById('graphSearch').value.trim() ? 'Связи по этому запросу не найдены.' : 'Назначений пользователей и групп не найдено. Создайте RoleBinding или ClusterRoleBinding с субъектом User или Group.';
    const plural = (count, forms) => `${count} ${forms[count % 100 >= 11 && count % 100 <= 14 ? 2 : count % 10 === 1 ? 0 : count % 10 >= 2 && count % 10 <= 4 ? 1 : 2]}`;
    document.getElementById('graphCount').textContent = [plural(nodes.filter(node => node.column === 0).length, ['субъект', 'субъекта', 'субъектов']), plural(nodes.filter(node => node.column === 1).length, ['назначение', 'назначения', 'назначений']), plural(nodes.filter(node => node.column === 2).length, ['роль', 'роли', 'ролей'])].join(' · ');
    for (const [index, text] of ['Пользователи и группы', 'Назначения', 'Роли'].entries()) {
      const label = document.createElement('div');
      label.className = 'graph-column-label';
      label.textContent = index === 0 && document.getElementById('graphAccounts').checked ? 'Субъекты' : text;
      label.style.left = `${40 + index * 370}px`;
      this.nodesLayer.append(label);
    }
    for (const node of nodes) {
      const position = this.positions.get(node.id);
      if (position) Object.assign(node, position);
      const button = document.createElement('button');
      button.className = 'graph-node graph-column-' + node.column + (node.missing ? ' graph-missing' : '');
      button.dataset.node = node.id;
      button.setAttribute('aria-label', `${node.kind}: ${node.name}${node.namespace ? ', ' + node.namespace : ''}${node.missing ? ', роль не найдена' : ''}`);
      const kind = document.createElement('span');
      kind.className = 'graph-node-kind';
      kind.textContent = node.kind;
      const name = document.createElement('strong');
      name.textContent = node.name;
      name.title = node.name;
      const scope = document.createElement('span');
      scope.className = 'graph-node-scope';
      scope.textContent = node.missing ? 'Роль не найдена' : node.namespace || (node.column === 0 ? 'Субъект кластера' : 'Весь кластер');
      button.append(kind, name, scope);
      button.style.left = node.x + 'px';
      button.style.top = node.y + 'px';
      button.addEventListener('click', event => {
        if (this.suppressClick) { event.preventDefault(); this.suppressClick = false; return; }
        this.select(node);
      });
      this.nodeElements.set(node.id, button);
      this.nodesLayer.append(button);
    }
    const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs');
    const marker = document.createElementNS(defs.namespaceURI, 'marker');
    for (const [key, value] of Object.entries({id: 'graph-arrow', viewBox: '0 0 10 10', refX: '9', refY: '5', markerWidth: '6', markerHeight: '6', orient: 'auto-start-reverse'})) marker.setAttribute(key, value);
    const arrow = document.createElementNS(defs.namespaceURI, 'path');
    arrow.setAttribute('d', 'M 0 0 L 10 5 L 0 10 z');
    arrow.setAttribute('fill', '#8a98a8');
    marker.append(arrow);
    defs.append(marker);
    this.svg.append(defs);
    for (const edge of edges) {
      const path = document.createElementNS(defs.namespaceURI, 'path');
      path.classList.add('graph-edge');
      path.setAttribute('marker-end', 'url(#graph-arrow)');
      this.svg.append(path);
      this.edgeElements.set(edge.id, path);
    }
    this.drawEdges();
    this.fit();
  }

  drawEdges() {
    const map = new Map(this.graph.nodes.map(node => [node.id, node]));
    for (const edge of this.graph.edges) {
      const from = map.get(edge.from), to = map.get(edge.to);
      const x1 = from.x + 280, y1 = from.y + 45, x2 = to.x, y2 = to.y + 45;
      const bend = Math.max(45, Math.abs(x2 - x1) / 2);
      this.edgeElements.get(edge.id).setAttribute('d', `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`);
    }
  }

  transform() {
    this.world.style.transform = `translate(${this.x}px, ${this.y}px) scale(${this.scale})`;
    document.getElementById('graphZoom').textContent = Math.round(this.scale * 100) + '%';
  }

  zoom(factor, cx = this.canvas.clientWidth / 2, cy = this.canvas.clientHeight / 2) {
    const scale = Math.max(.1, Math.min(2.5, this.scale * factor));
    const ratio = scale / this.scale;
    this.x = cx - (cx - this.x) * ratio;
    this.y = cy - (cy - this.y) * ratio;
    this.scale = scale;
    this.transform();
  }

  fit() {
    const nodes = this.graph.nodes;
    const minX = Math.min(0, ...nodes.map(node => node.x));
    const minY = Math.min(0, ...nodes.map(node => node.y));
    const width = Math.max(this.graph.width, ...nodes.map(node => node.x + 310)) - minX;
    const height = Math.max(220, ...nodes.map(node => node.y + 120)) - minY;
    this.scale = Math.max(.1, Math.min(1, (this.canvas.clientWidth - 40) / width, (this.canvas.clientHeight - 70) / height));
    this.x = (this.canvas.clientWidth - width * this.scale) / 2 - minX * this.scale;
    this.y = (this.canvas.clientHeight - height * this.scale) / 2 - minY * this.scale;
    this.transform();
  }

  startDrag(event) {
    if (event.button !== 0 || event.target.closest('.graph-controls')) return;
    this.suppressClick = false;
    const nodeID = event.target.closest('[data-node]')?.dataset.node;
    const node = this.graph.nodes.find(node => node.id === nodeID);
    this.drag = {pointer: event.pointerId, startX: event.clientX, startY: event.clientY, x: node ? node.x : this.x, y: node ? node.y : this.y, node, moved: false};
    if (!node) this.canvas.setPointerCapture(event.pointerId);
    this.canvas.classList.add('dragging');
  }

  moveDrag(event) {
    if (!this.drag || this.drag.pointer !== event.pointerId) return;
    const dx = event.clientX - this.drag.startX, dy = event.clientY - this.drag.startY;
    if (!this.drag.moved && Math.hypot(dx, dy) < 4) return;
    if (!this.drag.moved) this.canvas.setPointerCapture(event.pointerId);
    this.drag.moved = true;
    if (this.drag.node) {
      const node = this.drag.node;
      node.x = this.drag.x + dx / this.scale;
      node.y = this.drag.y + dy / this.scale;
      this.positions.set(node.id, {x: node.x, y: node.y});
      const element = this.nodeElements.get(node.id);
      element.style.left = node.x + 'px';
      element.style.top = node.y + 'px';
      this.drawEdges();
    } else {
      this.x = this.drag.x + dx;
      this.y = this.drag.y + dy;
      this.transform();
    }
  }

  endDrag(event) {
    if (!this.drag || this.drag.pointer !== event.pointerId) return;
    this.suppressClick = this.drag.moved;
    const node = this.drag.node;
    const moved = this.drag.moved;
    this.cancelDrag();
    if (this.canvas.hasPointerCapture(event.pointerId)) this.canvas.releasePointerCapture(event.pointerId);
    if (!node && !moved) this.select(null);
  }

  cancelDrag() { this.drag = null; this.canvas.classList.remove('dragging'); }

  select(node) {
    this.selected = node?.id || null;
    const highlighted = new Set();
    for (const chain of this.graph.chains) {
      const ids = Object.values(chain);
      if (ids.includes(this.selected)) ids.forEach(id => highlighted.add(id));
    }
    for (const [id, element] of this.nodeElements) {
      element.classList.toggle('selected', id === this.selected);
      element.classList.toggle('dimmed', !!node && !highlighted.has(id));
    }
    const selectedEdges = new Set();
    for (const chain of this.graph.chains) {
      if (!Object.values(chain).includes(this.selected)) continue;
      selectedEdges.add(JSON.stringify([chain.subject, chain.binding]));
      selectedEdges.add(JSON.stringify([chain.binding, chain.role]));
    }
    for (const [id, element] of this.edgeElements) {
      element.classList.toggle('highlighted', selectedEdges.has(id));
      element.classList.toggle('dimmed', !!node && !selectedEdges.has(id));
    }
    const panel = document.getElementById('graphDetails');
    panel.replaceChildren();
    panel.hidden = !node;
    if (!node) return;
    const addText = (tag, text, className) => {
      const element = document.createElement(tag);
      element.textContent = text;
      if (className) element.className = className;
      panel.append(element);
      return element;
    };
    const close = addText('button', '×', 'graph-close');
    close.setAttribute('aria-label', 'Закрыть детали');
    close.addEventListener('click', () => this.select(null));
    addText('div', node.kind, 'eyebrow');
    addText('h2', node.name);
    if (node.namespace) addText('p', 'Namespace: ' + node.namespace);
    if (node.missing) { addText('p', 'Роль, на которую ссылается назначение, не найдена.', 'error'); return; }
    if (node.column === 0) {
      const bindings = [...new Set(this.graph.chains.filter(chain => chain.subject === node.id).map(chain => chain.binding))];
      addText('h3', 'Назначения');
      for (const id of bindings) {
        const binding = this.graph.nodes.find(item => item.id === id);
        const button = addText('button', binding.name, 'graph-related');
        button.addEventListener('click', () => this.select(binding));
      }
      if (node.kind === 'Group') addText('p', 'Состав группы определяется внешней системой аутентификации.');
    } else {
      const button = addText('button', 'Открыть ресурс');
      button.addEventListener('click', () => this.openResource(node.object));
      if (node.object.roleRef) {
        addText('h3', 'Роль');
        addText('p', node.object.roleRef.kind + '/' + node.object.roleRef.name);
        addText('h3', 'Субъекты назначения');
        for (const subject of node.object.subjects || []) addText('p', `${subject.kind}: ${subject.namespace ? subject.namespace + '/' : ''}${subject.name}`);
      } else {
        addText('h3', 'Разрешения');
        for (const rule of node.object.rules || []) {
          addText('div', (rule.resources || rule.nonResourceURLs || []).join(', '), 'graph-rule-title');
          addText('p', (rule.verbs || []).join(', '));
          if (rule.apiGroups) addText('p', 'API: ' + rule.apiGroups.map(group => group || 'core').join(', '));
          if (rule.resourceNames?.length) addText('p', 'Имена: ' + rule.resourceNames.join(', '));
        }
        if (!node.object.rules?.length) addText('p', 'Разрешения не заданы.');
        if (node.object.aggregationRule) addText('p', 'Агрегируемая ClusterRole. Показаны правила, рассчитанные Kubernetes.');
      }
    }
  }
}
