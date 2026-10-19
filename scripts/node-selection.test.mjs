import test from 'node:test';
import assert from 'node:assert/strict';
import { matchesCandidate, selectForTest, bestResult, provinces } from '../internal/app/web/node-selection.mjs';

const items = [
  { ip: '1.1.1.1', location: '广东深圳 联通' },
  { ip: '2.2.2.2', location: '广东省广州市 电信' },
  { ip: '3.3.3.3', location: '广西壮族自治区南宁 联通' },
  { ip: '4.4.4.4', location: '广东东莞 联通' },
  { ip: '5.5.5.5', location: '' }
];

test('search matches all space-separated terms anywhere in IP and location', () => {
  assert.equal(matchesCandidate(items[0], { query: '  广东\t 联通  ' }), true);
  assert.equal(matchesCandidate(items[0], { query: '联通 广东 1.1' }), true);
  assert.equal(matchesCandidate(items[1], { query: '广东 联通' }), false);
  assert.equal(matchesCandidate(items[2], { query: '广东 联通' }), false);
  assert.equal(matchesCandidate({ ip: '8.8.8.8', location: 'Hong Kong' }, { query: 'HONG kong' }), true);
  assert.equal(matchesCandidate(items[4], { query: '' }), true);
});

test('province and carrier filters combine with search and do not guess missing regions', () => {
  const filters = { province: '广东', carrier: '联通' };
  assert.deepEqual(selectForTest(items, filters).map(item => item.ip), ['1.1.1.1', '4.4.4.4']);
  assert.deepEqual(selectForTest(items, { ...filters, query: '深圳' }).map(item => item.ip), ['1.1.1.1']);
  assert.equal(matchesCandidate(items[1], { province: '广东' }), true);
  assert.equal(matchesCandidate(items[2], { province: '广西' }), true);
  assert.equal(matchesCandidate(items[4], { province: '广东' }), false);
  assert.deepEqual(selectForTest(items, { province: '新疆' }), []);
  assert.equal(provinces.length, 34);
});

test('automatic testing deduplicates and samples within its budget', () => {
  const many = Array.from({ length: 256 }, (_, n) => ({ ip: `8.8.${Math.floor(n / 255)}.${n % 255}`, location: '广东 联通' }));
  const picked = selectForTest([...many, ...many], { province: '广东' });
  assert.equal(picked.length, 16);
  assert.equal(new Set(picked.map(item => item.ip)).size, 16);
  assert.equal(picked[1].ip, many[16].ip);
  assert.equal(selectForTest(many, {}, 999).length, 128);
  assert.equal(selectForTest(many, {}, 8).length, 8);
});

test('recommendation uses only successful, finite, positive throughput', () => {
  const results = [
    { ip: 'error', mbps: 999, error: 'partial failure' },
    { ip: 'empty', mbps: 0 },
    { ip: 'invalid', mbps: Infinity },
    { ip: 'slow', mbps: 5, latencyMS: 3 },
    { ip: 'fast', mbps: 10, latencyMS: 8 },
    { ip: 'fastest', mbps: 10, latencyMS: 4 }
  ];
  assert.equal(bestResult(results).ip, 'fastest');
  assert.equal(bestResult(results.slice(0, 3)), null);
  assert.equal(bestResult([]), null);
});

test('sampling handles empty, unmatched, and unusual budget values', () => {
  const source = Array.from({ length: 140 }, (_, index) => ({ ip: `10.0.0.${index}`, location: '广东 联通' }));
  assert.deepEqual(selectForTest([], {}, 4), []);
  assert.deepEqual(selectForTest(source, { province: '新疆' }), []);
  assert.equal(selectForTest(source, {}, 1).length, 1);
  assert.equal(selectForTest(source, {}, 3.9).length, 3);
  assert.equal(selectForTest(source, {}, 0).length, 16); // Falsy budgets use the default of 16.
  assert.equal(selectForTest(source, {}, -2).length, 1);
  assert.equal(selectForTest(source, {}, Number.NaN).length, 16);
  assert.equal(selectForTest(source, {}, Number.POSITIVE_INFINITY).length, 128);
});

test('filtering happens before IP deduplication and sampling leaves input unchanged', () => {
  const input = [
    { ip: '10.1.0.1', location: '广西 联通' },
    { ip: '10.1.0.1', location: '广东 联通' },
    { ip: '10.1.0.2', location: '广东 联通' },
    { ip: '10.1.0.3', location: '广东 电信' }
  ];
  const before = structuredClone(input);
  const selected = selectForTest(input, { province: '广东', carrier: '联通' });
  assert.deepEqual(selected.map(item => item.ip), ['10.1.0.1', '10.1.0.2']);
  assert.equal(selected[0].location, '广东 联通');
  assert.deepEqual(input, before);
});

test('best result rejects invalid throughput and resolves ties without mutating input', () => {
  const results = [
    { ip: 'nan', mbps: Number.NaN, latencyMS: 1 },
    { ip: 'negative', mbps: -1, latencyMS: 1 },
    { ip: 'string', mbps: '100', latencyMS: 1 },
    { ip: 'failed', mbps: 100, error: 'request failed', latencyMS: 1 },
    { ip: 'tie-first', mbps: 12, latencyMS: 5 },
    { ip: 'faster-tie', mbps: 12, latencyMS: 2 },
    { ip: 'stable-tie', mbps: 12, latencyMS: 2 }
  ];
  const before = structuredClone(results);
  assert.equal(bestResult(results).ip, 'faster-tie');
  assert.deepEqual(results, before);
  assert.equal(bestResult(results.slice(0, 4)), null);
});
