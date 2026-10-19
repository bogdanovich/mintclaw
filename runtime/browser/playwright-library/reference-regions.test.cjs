'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { referenceRegions, findSnapshotRef, MAX_REGION_METADATA_BYTES } = require('./reference-regions.cjs');

const button = ref => ({ role: 'button', name: 'Remove account', ref });
const region = (name, children) => ({ role: 'region', name, children });

test('same-caption refs retain their nearest unique native named region', () => {
  const tree = [region('North account', [button('e1')]), region('South account', [button('e2')])];
  assert.deepEqual(referenceRegions(tree), {
    version: 1, complete: true,
    entries: [{ ref: 'e1', name: 'North account' }, { ref: 'e2', name: 'South account' }],
  });
  assert.equal(findSnapshotRef(tree, 'e2'), tree[1].children[0]);
  const nested = [region('Outer', [region('Inner', [button('e3')]), button('e4')])];
  assert.deepEqual(referenceRegions(nested).entries, [{ ref: 'e3', name: 'Inner' }, { ref: 'e4', name: 'Outer' }]);
});

test('captions, positions, duplicate labels and frames do not invent region authority', () => {
  assert.deepEqual(referenceRegions([button('e1')]).entries, []);
  const duplicates = [region('South', [button('e1')]), region('South', [button('e2')])];
  assert.deepEqual(referenceRegions(duplicates).entries, []);
  const frames = [region('Outer', [{ role: 'iframe', children: [region('Inner', [button('e1')])] }, button('f1e2')])];
  assert.deepEqual(referenceRegions(frames).entries, []);
  assert.equal(findSnapshotRef(frames, 'e1'), null);
  const repeated = [region('One', [button('e1'), button('e1')])];
  assert.equal(referenceRegions(repeated).complete, false);
  assert.equal(findSnapshotRef(repeated, 'e1'), null);
});

test('size, node, ref and depth limits discard all partial qualification', () => {
  for (const tree of [
    [region('South', Array.from({ length: 33 }, (_, index) => button(`e${index + 1}`)))],
    [region('South', Array.from({ length: 4100 }, () => 'text'))],
    Array.from({ length: 16 }, (_, index) => region(`${'x'.repeat(500)}${index}`, [button(`e${index + 1}`)])),
  ]) {
    const result = referenceRegions(tree);
    assert.equal(result.complete, false);
    assert.deepEqual(result.entries, []);
    assert.ok(Buffer.byteLength(JSON.stringify(result)) <= MAX_REGION_METADATA_BYTES);
  }
  let tree = [button('e1')];
  for (let depth = 0; depth < 35; depth++) tree = [{ role: 'group', children: tree }];
  assert.equal(referenceRegions(tree).complete, false);
  assert.equal(findSnapshotRef(tree, 'e1'), null);
  assert.deepEqual(referenceRegions([region('Outer', [region('x'.repeat(513), [button('e1')])])]).entries, []);
});
