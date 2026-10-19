// Location labels come from the candidate source, never from browser geolocation.
export const provinces = [
  '北京', '天津', '河北', '山西', '内蒙古', '辽宁', '吉林', '黑龙江',
  '上海', '江苏', '浙江', '安徽', '福建', '江西', '山东', '河南',
  '湖北', '湖南', '广东', '广西', '海南', '重庆', '四川', '贵州',
  '云南', '西藏', '陕西', '甘肃', '青海', '宁夏', '新疆', '台湾', '香港', '澳门'
];

export function matchesCandidate(item, { query = '', province = '', carrier = '' } = {}) {
  const location = String(item.location || '').toLocaleLowerCase();
  const text = `${item.ip || ''} ${location}`.toLocaleLowerCase();
  const terms = query.trim().toLocaleLowerCase().split(/\s+/u).filter(Boolean);
  return (!province || location.includes(province)) &&
    (!carrier || location.includes(carrier)) && terms.every(term => text.includes(term));
}

// Sample across the source order instead of spending the entire budget on its first block.
export function selectForTest(items, filters, limit = 16) {
  const unique = [...new Map(items.filter(item => matchesCandidate(item, filters))
    .map(item => [item.ip, item])).values()];
  const count = Math.min(unique.length, Math.max(1, Math.min(128, Math.floor(limit) || 16)));
  return Array.from({ length: count }, (_, index) => unique[Math.floor(index * unique.length / count)]);
}

export function bestResult(results) {
  return [...results].filter(result => !result.error && Number.isFinite(result.mbps) && result.mbps > 0)
    .sort((a, b) => b.mbps - a.mbps || a.latencyMS - b.latencyMS)[0] || null;
}
