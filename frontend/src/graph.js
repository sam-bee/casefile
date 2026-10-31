const NS = 'http://www.w3.org/2000/svg';
const WIDTH = 1000;
const HEIGHT = 650;
const CENTER_X = WIDTH / 2;
const CENTER_Y = HEIGHT / 2;

function svgElement(tag, className = '') {
  const element = document.createElementNS(NS, tag);
  if (className) element.setAttribute('class', className);
  return element;
}

function clamp(value, minimum, maximum) {
  return Math.min(maximum, Math.max(minimum, value));
}

function labelFor(node) {
  const title = String(node.title || 'Untitled casenote');
  return title.length > 28 ? `${title.slice(0, 27)}…` : title;
}

// A small force layout keeps the graph self-contained: no browser CDN or host-side JS build.
export function mountGraph(container, rawNodes, rawEdges, onOpen, viewState = null) {
  container.replaceChildren();
  const sparseGraph = rawNodes.length <= 8;
  const nodes = rawNodes.map((casenote, index) => {
    const angle = index * 2.399963229728653;
    const radius = Math.sqrt(index) * (sparseGraph ? 110 : 54);
    return {
      ...casenote,
      id: Number(casenote.id),
      x: CENTER_X + Math.cos(angle) * radius,
      y: CENTER_Y + Math.sin(angle) * radius,
      vx: 0, vy: 0, degree: 0,
    };
  });
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const indexById = new Map(nodes.map((node, index) => [node.id, index]));
  const edges = rawEdges.map((edge) => ({
    ...edge,
    source: byId.get(Number(edge.from_casenote_id)),
    target: byId.get(Number(edge.to_casenote_id)),
    sourceIndex: indexById.get(Number(edge.from_casenote_id)),
    targetIndex: indexById.get(Number(edge.to_casenote_id)),
    weightValue: edge.weight == null ? null : clamp(Number(edge.weight) || 0, 0, 1),
  })).filter((edge) => edge.source && edge.target);
  edges.forEach((edge) => { edge.source.degree++; edge.target.degree++; });

  const svg = svgElement('svg', 'graph-svg');
  if (sparseGraph) svg.classList.add('is-sparse');
  if (nodes.length > 40) svg.classList.add('is-dense');
  svg.setAttribute('viewBox', `0 0 ${WIDTH} ${HEIGHT}`);
  svg.setAttribute('role', 'group');
  svg.setAttribute('aria-label', `${nodes.length} research casenote${nodes.length === 1 ? '' : 's'} connected by ${edges.length} link${edges.length === 1 ? '' : 's'}`);
  const defs = svgElement('defs');
  const pattern = svgElement('pattern');
  pattern.id = 'graphGrid';
  pattern.setAttribute('width', '26');
  pattern.setAttribute('height', '26');
  pattern.setAttribute('patternUnits', 'userSpaceOnUse');
  const gridDot = svgElement('circle');
  gridDot.setAttribute('cx', '1'); gridDot.setAttribute('cy', '1'); gridDot.setAttribute('r', '1');
  gridDot.setAttribute('fill', '#29485a');
  pattern.append(gridDot); defs.append(pattern);
  svg.append(defs);
  const backdrop = svgElement('rect', 'graph-backdrop');
  backdrop.setAttribute('width', String(WIDTH));
  backdrop.setAttribute('height', String(HEIGHT));
  backdrop.setAttribute('fill', 'url(#graphGrid)');
  svg.append(backdrop);
  const world = svgElement('g', 'graph-world');
  const edgeLayer = svgElement('g', 'graph-edges');
  const nodeLayer = svgElement('g', 'graph-nodes');
  world.append(edgeLayer, nodeLayer); svg.append(world);
  container.append(svg);

  const initialScale = sparseGraph ? 1.6 : 1;
  let scale = Number.isFinite(viewState?.scale) ? clamp(viewState.scale, 0.4, 3.5) : initialScale;
  let translateX = Number.isFinite(viewState?.translateX) ? viewState.translateX : CENTER_X * (1 - scale);
  let translateY = Number.isFinite(viewState?.translateY) ? viewState.translateY : CENTER_Y * (1 - scale);
  let active = null, hovered = null;
  let animationFrame = 0, ticksRemaining = 180, destroyed = false;
  const edgeElements = edges.map((edge) => {
    const line = svgElement('line', 'graph-edge');
    line.style.strokeWidth = String(1 + (edge.weightValue ?? 0.3) * 2.8);
    line.style.strokeOpacity = String(0.28 + (edge.weightValue ?? 0.3) * 0.48);
    const title = svgElement('title');
    title.textContent = `#${edge.source.id} ${edge.relation || 'linked to'} #${edge.target.id}${edge.weightValue == null ? '' : ` · weight ${edge.weightValue}`}${edge.note ? ` · ${edge.note}` : ''}`;
    line.append(title);
    edgeLayer.append(line);
    return line;
  });
  const nodeElements = nodes.map((node) => {
    const group = svgElement('g', `graph-node graph-kind-${String(node.kind || 'other').replace(/[^a-z0-9-]/gi, '-')}`);
    group.setAttribute('tabindex', '0');
    group.setAttribute('role', 'button');
    group.setAttribute('aria-label', `Open #${node.id}: ${node.title || 'Untitled casenote'}`);
    const halo = svgElement('circle', 'graph-node-halo');
    halo.setAttribute('r', String((sparseGraph ? 18 : 14) + Math.min(node.degree, 8) * 1.5));
    const circle = svgElement('circle', 'graph-node-core');
    circle.setAttribute('r', String((sparseGraph ? 11 : 8) + Math.min(node.degree, 8) * 1.5));
    const idLabel = svgElement('text', 'graph-node-id');
    idLabel.setAttribute('x', '0'); idLabel.setAttribute('y', '3');
    idLabel.textContent = `#${node.id}`;
    const titleLabel = svgElement('text', 'graph-node-label');
    titleLabel.setAttribute('x', '0');
    titleLabel.setAttribute('y', String((sparseGraph ? 32 : 27) + Math.min(node.degree, 8) * 1.5));
    titleLabel.textContent = labelFor(node);
    const title = svgElement('title');
    title.textContent = `#${node.id} · ${node.title || 'Untitled casenote'} · ${node.kind || 'casenote'} · ${node.status || 'unknown'}`;
    group.append(halo, circle, idLabel, titleLabel, title);
    group.addEventListener('pointerdown', (event) => {
      event.preventDefault(); event.stopPropagation();
      const pointer = point(event);
      active = { type: 'node', node, x: pointer.x, y: pointer.y, moved: false };
      svg.setPointerCapture(event.pointerId);
      group.classList.add('is-dragging');
    });
    group.addEventListener('pointerenter', () => setHovered(node));
    group.addEventListener('pointerleave', () => { if (hovered === node) setHovered(null); });
    group.addEventListener('focus', () => setHovered(node));
    group.addEventListener('blur', () => { if (hovered === node) setHovered(null); });
    group.addEventListener('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); onOpen(node.id); }
    });
    nodeLayer.append(group);
    return group;
  });

  const info = document.createElement('div');
  info.className = 'graph-inspector';
  info.setAttribute('aria-live', 'polite');
  container.append(info);
  const defaultInfo = `${nodes.length} casenote${nodes.length === 1 ? '' : 's'} · ${edges.length} connection${edges.length === 1 ? '' : 's'}. Drag nodes, drag the canvas to pan, scroll to zoom.`;
  info.textContent = defaultInfo;

  function setHovered(node) {
    hovered = node;
    if (!node) {
      info.textContent = defaultInfo;
      nodeElements.forEach((element) => element.classList.remove('is-dimmed', 'is-neighbor', 'is-active'));
      edgeElements.forEach((element) => element.classList.remove('is-dimmed', 'is-active'));
      return;
    }
    const neighbors = new Set([node.id]);
    edges.forEach((edge) => {
      if (edge.source === node) neighbors.add(edge.target.id);
      if (edge.target === node) neighbors.add(edge.source.id);
    });
    nodeElements.forEach((element, index) => {
      element.classList.toggle('is-active', nodes[index] === node);
      element.classList.toggle('is-neighbor', nodes[index] !== node && neighbors.has(nodes[index].id));
      element.classList.toggle('is-dimmed', !neighbors.has(nodes[index].id));
    });
    edgeElements.forEach((element, index) => {
      const connected = edges[index].source === node || edges[index].target === node;
      element.classList.toggle('is-active', connected);
      element.classList.toggle('is-dimmed', !connected);
    });
    info.replaceChildren();
    const heading = document.createElement('strong');
    heading.textContent = `#${node.id} ${node.title || 'Untitled casenote'}`;
    const detail = document.createElement('span');
    detail.textContent = `${node.kind || 'casenote'} · ${node.status || 'unknown'} · ${node.degree} connection${node.degree === 1 ? '' : 's'} · click to open`;
    info.append(heading, detail);
    const connections = edges.filter((edge) => edge.source === node || edge.target === node).slice(0, 4);
    if (connections.length) {
      const list = document.createElement('div');
      list.className = 'graph-inspector-links';
      connections.forEach((edge) => {
        const line = document.createElement('span');
        const other = edge.source === node ? edge.target : edge.source;
        line.textContent = `#${other.id} ${edge.relation || 'linked'}${edge.weightValue == null ? '' : ` · weight ${edge.weightValue}`}`;
        list.append(line);
      });
      info.append(list);
    }
  }

  function point(event) {
    const p = svg.createSVGPoint();
    p.x = event.clientX; p.y = event.clientY;
    return p.matrixTransform(svg.getScreenCTM().inverse());
  }

  function updateTransform() {
    world.setAttribute('transform', `translate(${translateX} ${translateY}) scale(${scale})`);
  }

  function draw() {
    edgeElements.forEach((line, index) => {
      const edge = edges[index];
      line.setAttribute('x1', String(edge.source.x));
      line.setAttribute('y1', String(edge.source.y));
      line.setAttribute('x2', String(edge.target.x));
      line.setAttribute('y2', String(edge.target.y));
    });
    nodeElements.forEach((group, index) => {
      group.setAttribute('transform', `translate(${nodes[index].x} ${nodes[index].y})`);
    });
  }

  function step() {
    if (destroyed || ticksRemaining-- <= 0) { animationFrame = 0; return; }
    const forceX = new Float64Array(nodes.length);
    const forceY = new Float64Array(nodes.length);
    for (let i = 0; i < nodes.length; i++) {
      forceX[i] = (CENTER_X - nodes[i].x) * (sparseGraph ? 0.0007 : 0.0012);
      forceY[i] = (CENTER_Y - nodes[i].y) * (sparseGraph ? 0.0007 : 0.0012);
      for (let j = i + 1; j < nodes.length; j++) {
        const dx = nodes[j].x - nodes[i].x;
        const dy = nodes[j].y - nodes[i].y;
        const distance2 = Math.max(140, dx * dx + dy * dy);
        const push = (sparseGraph ? 12000 : 8500) / distance2;
        const distance = Math.sqrt(distance2);
        const fx = dx / distance * push;
        const fy = dy / distance * push;
        forceX[i] -= fx; forceY[i] -= fy;
        forceX[j] += fx; forceY[j] += fy;
      }
    }
    for (const edge of edges) {
      const dx = edge.target.x - edge.source.x;
      const dy = edge.target.y - edge.source.y;
      const distance = Math.max(1, Math.hypot(dx, dy));
      const ideal = (sparseGraph ? 225 : 140) - (edge.weightValue ?? 0.3) * 45;
      const strength = (distance - ideal) * (0.006 + (edge.weightValue ?? 0.3) * 0.006);
      const fx = dx / distance * strength;
      const fy = dy / distance * strength;
      forceX[edge.sourceIndex] += fx; forceY[edge.sourceIndex] += fy;
      forceX[edge.targetIndex] -= fx; forceY[edge.targetIndex] -= fy;
    }
    nodes.forEach((node, index) => {
      if (active?.type === 'node' && active.node === node) return;
      node.vx = clamp((node.vx + forceX[index]) * 0.84, -8, 8);
      node.vy = clamp((node.vy + forceY[index]) * 0.84, -8, 8);
      node.x = clamp(node.x + node.vx, 35, WIDTH - 35);
      node.y = clamp(node.y + node.vy, 35, HEIGHT - 35);
    });
    draw();
    animationFrame = requestAnimationFrame(step);
  }

  function restart() {
    ticksRemaining = 90;
    if (!animationFrame) animationFrame = requestAnimationFrame(step);
  }

  function zoom(factor, pointer = { x: CENTER_X, y: CENTER_Y }) {
    const next = clamp(scale * factor, 0.4, 3.5);
    const ratio = next / scale;
    translateX = pointer.x - (pointer.x - translateX) * ratio;
    translateY = pointer.y - (pointer.y - translateY) * ratio;
    scale = next;
    updateTransform();
  }

  svg.addEventListener('pointerdown', (event) => {
    if (event.target.closest('.graph-node')) return;
    const pointer = point(event);
    active = { type: 'pan', x: pointer.x, y: pointer.y };
    svg.setPointerCapture(event.pointerId);
    svg.classList.add('is-panning');
  });
  svg.addEventListener('pointermove', (event) => {
    if (!active) return;
    const pointer = point(event);
    const dx = pointer.x - active.x;
    const dy = pointer.y - active.y;
    if (active.type === 'node') {
      if (Math.abs(dx) + Math.abs(dy) > 2) active.moved = true;
      active.node.x = clamp(active.node.x + dx / scale, 25, WIDTH - 25);
      active.node.y = clamp(active.node.y + dy / scale, 25, HEIGHT - 25);
      active.node.vx = 0; active.node.vy = 0;
      draw();
    } else {
      translateX += dx; translateY += dy;
      updateTransform();
    }
    active.x = pointer.x; active.y = pointer.y;
  });
  const endPointer = (event) => {
    if (!active) return;
    const previous = active;
    active = null;
    svg.classList.remove('is-panning');
    nodeElements.forEach((element) => element.classList.remove('is-dragging'));
    if (previous.type === 'node') {
      if (!previous.moved) onOpen(previous.node.id);
      else restart();
    }
    if (svg.hasPointerCapture(event.pointerId)) svg.releasePointerCapture(event.pointerId);
  };
  svg.addEventListener('pointerup', endPointer);
  svg.addEventListener('pointercancel', endPointer);
  svg.addEventListener('wheel', (event) => {
    event.preventDefault();
    zoom(event.deltaY < 0 ? 1.12 : 1 / 1.12, point(event));
  }, { passive: false });

  draw(); updateTransform(); restart();
  return {
    getViewState: () => ({ scale, translateX, translateY }),
    zoomIn: () => zoom(1.25),
    zoomOut: () => zoom(0.8),
    reset: () => { scale = initialScale; translateX = CENTER_X * (1 - initialScale); translateY = CENTER_Y * (1 - initialScale); updateTransform(); },
    destroy: () => { destroyed = true; if (animationFrame) cancelAnimationFrame(animationFrame); container.replaceChildren(); },
  };
}
