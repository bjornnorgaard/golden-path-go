// Finds out how much a single instance can take: a constant, high number of virtual users
// hammers the REST todo lifecycle with no think time, so throughput is limited by the server
// (and its database), not by the ramp.
//
//   VUS=64 DURATION=30s k6 run k6/rest-saturation.js
import http from 'k6/http';
import { check } from 'k6';
import { BASE_URL, jsonHeaders, randomTitle, futureDueBy, saveSummary } from './lib/helpers.js';

export const options = {
  scenarios: {
    saturate: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 64),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: { http_req_failed: ['rate<0.01'] },
};

export default function () {
  const create = http.post(`${BASE_URL}/todos/create`, JSON.stringify({ title: randomTitle('sat'), dueBy: futureDueBy(30) }), jsonHeaders());
  const id = create.json('id');
  check(create, { 'create: 200': (r) => r.status === 200 });
  if (!id) return;
  check(http.post(`${BASE_URL}/todos/get-by-id`, JSON.stringify({ id }), jsonHeaders()), { 'get: 200': (r) => r.status === 200 });
  check(http.post(`${BASE_URL}/todos/get-list`, JSON.stringify({ limit: 20, offset: 0 }), jsonHeaders()), { 'list: 200': (r) => r.status === 200 });
  check(http.post(`${BASE_URL}/todos/toggle`, JSON.stringify({ id }), jsonHeaders()), { 'toggle: 200': (r) => r.status === 200 });
  check(http.post(`${BASE_URL}/todos/delete`, JSON.stringify({ id }), jsonHeaders()), { 'delete: 200': (r) => r.status === 200 });
}

export function handleSummary(data) {
  return saveSummary('rest-saturation', data);
}
