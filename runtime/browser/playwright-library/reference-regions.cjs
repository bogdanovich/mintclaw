'use strict';

const MAX_REGION_METADATA_BYTES = 4096;
const MAX_REGION_ENTRIES = 32;
const MAX_REGION_NAME_BYTES = 512;
const MAIN_REF = /^e[1-9][0-9]{0,9}$/;

// A label is contextual evidence, never permission. Unsupported/large trees
// return no qualified entries rather than guessing from captions or position.
function referenceRegions(tree) {
  const counts = new Map();
  const candidates = [];
  const refs = new Set();
  let nodes = 0;
  let complete = true;
  const walk = (items, region, depth) => {
    if (!Array.isArray(items) || depth > 32) { complete = false; return; }
    for (const node of items) {
      if (++nodes > 4096) { complete = false; return; }
      if (typeof node === 'string') continue;
      if (!node || typeof node !== 'object') { complete = false; return; }
      if (node.role === 'iframe') continue;
      let current = region;
      if (node.role === 'region' && typeof node.name === 'string' && node.name.trim()) {
        current = Buffer.byteLength(node.name, 'utf8') <= MAX_REGION_NAME_BYTES ? node.name : null;
        if (current) counts.set(current, (counts.get(current) || 0) + 1);
      }
      if (node.role === 'button' && MAIN_REF.test(node.ref || '') && current) {
        if (refs.has(node.ref)) { complete = false; return; }
        refs.add(node.ref);
        candidates.push({ ref: node.ref, name: current });
        if (candidates.length > MAX_REGION_ENTRIES) { complete = false; return; }
      }
      if (node.children) walk(node.children, current, depth + 1);
      if (!complete) return;
    }
  };
  walk(tree, null, 0);
  let result = {
    version: 1, complete,
    entries: complete ? candidates.filter(entry => counts.get(entry.name) === 1) : [],
  };
  if (Buffer.byteLength(JSON.stringify(result), 'utf8') > MAX_REGION_METADATA_BYTES) {
    result = { version: 1, complete: false, entries: [] };
  }
  return result;
}

function findSnapshotRef(tree, target) {
  let found = null;
  let nodes = 0;
  let complete = true;
  const walk = (items, depth) => {
    if (!Array.isArray(items) || depth > 32) { complete = false; return; }
    for (const node of items) {
      if (++nodes > 4096) { complete = false; return; }
      if (typeof node === 'string') continue;
      if (!node || typeof node !== 'object') { complete = false; return; }
      if (node.role === 'iframe') continue;
      if (node.ref === target) {
        if (found) { complete = false; return; }
        found = node;
      }
      if (node.children) walk(node.children, depth + 1);
      if (!complete) return;
    }
  };
  walk(tree, 0);
  return complete ? found : null;
}

module.exports = { referenceRegions, findSnapshotRef, MAX_REGION_METADATA_BYTES };
