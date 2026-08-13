import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const root = new URL('../', import.meta.url);
const document = JSON.parse(readFileSync(new URL('protocol/openapi.json', root), 'utf8'));

const resolveRef = ref => {
  assert(ref.startsWith('#/'), `only local OpenAPI references are supported: ${ref}`);
  let value = document;
  for (const segment of ref.slice(2).split('/')) {
    value = value?.[segment.replaceAll('~1', '/').replaceAll('~0', '~')];
  }
  assert(value !== undefined, `unresolved OpenAPI reference: ${ref}`);
  return value;
};

const visit = value => {
  if (Array.isArray(value)) {
    value.forEach(visit);
    return;
  }
  if (typeof value !== 'object' || value === null) return;
  if (typeof value.$ref === 'string') resolveRef(value.$ref);
  Object.values(value).forEach(visit);
};
visit(document);

const valid = (schema, value) => {
  if (schema === false) return { ok: false, properties: new Set() };
  if (schema.$ref) return valid(resolveRef(schema.$ref), value);
  if (schema.const !== undefined && value !== schema.const) return { ok: false, properties: new Set() };
  if (schema.enum && !schema.enum.includes(value)) return { ok: false, properties: new Set() };
  if (schema.oneOf) {
    const matches = schema.oneOf.map(candidate => valid(candidate, value)).filter(result => result.ok);
    return matches.length === 1 ? matches[0] : { ok: false, properties: new Set() };
  }

  const evaluated = new Set();
  if (schema.allOf) {
    for (const candidate of schema.allOf) {
      const result = valid(candidate, value);
      if (!result.ok) return result;
      result.properties.forEach(property => evaluated.add(property));
    }
  }

  const types = Array.isArray(schema.type) ? schema.type : schema.type === undefined ? [] : [schema.type];
  if (types.length > 0 && !types.some(type => matchesType(type, value))) {
    return { ok: false, properties: evaluated };
  }
  if (typeof value === 'string') {
    if ((schema.minLength ?? 0) > value.length || value.length > (schema.maxLength ?? Infinity)) return { ok: false, properties: evaluated };
    if (schema.pattern && !new RegExp(schema.pattern, 'u').test(value)) return { ok: false, properties: evaluated };
  }
  if (typeof value === 'number') {
    if (schema.type === 'integer' && !Number.isInteger(value)) return { ok: false, properties: evaluated };
    if (value < (schema.minimum ?? -Infinity) || value > (schema.maximum ?? Infinity)) return { ok: false, properties: evaluated };
    if (schema.exclusiveMinimum !== undefined && value <= schema.exclusiveMinimum) return { ok: false, properties: evaluated };
  }
  if (Array.isArray(value)) {
    if (value.length < (schema.minItems ?? 0) || value.length > (schema.maxItems ?? Infinity)) return { ok: false, properties: evaluated };
    if (schema.items && value.some(item => !valid(schema.items, item).ok)) return { ok: false, properties: evaluated };
  }
  if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
    const required = schema.required ?? [];
    if (required.some(property => !Object.hasOwn(value, property))) return { ok: false, properties: evaluated };
    for (const [property, propertySchema] of Object.entries(schema.properties ?? {})) {
      if (!Object.hasOwn(value, property)) continue;
      evaluated.add(property);
      if (!valid(propertySchema, value[property]).ok) return { ok: false, properties: evaluated };
    }
    const extras = Object.keys(value).filter(property => !evaluated.has(property));
    if (schema.additionalProperties === false && extras.length > 0) return { ok: false, properties: evaluated };
    if (typeof schema.additionalProperties === 'object') {
      for (const property of extras) {
        if (!valid(schema.additionalProperties, value[property]).ok) return { ok: false, properties: evaluated };
        evaluated.add(property);
      }
    }
    if (schema.unevaluatedProperties === false && extras.length > 0) return { ok: false, properties: evaluated };
  }
  return { ok: true, properties: evaluated };
};

function matchesType(type, value) {
  switch (type) {
    case 'object': return typeof value === 'object' && value !== null && !Array.isArray(value);
    case 'array': return Array.isArray(value);
    case 'string': return typeof value === 'string';
    case 'integer': return typeof value === 'number' && Number.isInteger(value);
    case 'number': return typeof value === 'number' && Number.isFinite(value);
    case 'boolean': return typeof value === 'boolean';
    case 'null': return value === null;
    default: throw new Error(`unsupported schema type: ${type}`);
  }
}

const fixtureCases = [
  ['LobbyCheckInRequest', 'valid/lobby-request.json', true],
  ['LobbyLeaveRequest', 'valid/lobby-leave-request.json', true],
  ['LobbyLeaveRequest', 'invalid/lobby-leave-request-extra-fields.json', false],
  ['LobbyResponse', 'valid/lobby-response.json', true],
  ['LobbyResponse', 'invalid/lobby-response-missing-token.json', false],
  ['LobbyResponse', 'invalid/lobby-response-zero-port.json', false],
  ['MatchmakingRequest', 'valid/matchmaking-request.json', true],
  ['MatchmakingRequest', 'invalid/matchmaking-request-extra-metadata.json', false],
  ['MatchmakingRequest', 'invalid/matchmaking-request-bad-match-code.json', false],
  ['MatchmakingCancelRequest', 'valid/matchmaking-cancel-request.json', true],
  ['MatchmakingCancelRequest', 'invalid/matchmaking-cancel-request-extra-fields.json', false],
  ['MatchmakingOutcomeRequest', 'valid/matchmaking-outcome-request.json', true],
  ['MatchmakingResponse', 'valid/matchmaking-waiting-response.json', true],
  ['MatchmakingResponse', 'valid/matchmaking-matched-response.json', true],
  ['MatchmakingResponse', 'valid/matchmaking-canceled-response.json', true],
  ['MatchmakingResponse', 'invalid/matchmaking-matched-response-bad-port.json', false],
  ['EventsResponse', 'valid/events-response.json', true],
  ['EventsResponse', 'invalid/events-response-zero-duration.json', false],
  ['HealthResponse', 'valid/health-response.json', true],
  ['CrashReportRequest', 'valid/crash-report-request.json', true],
  ['FeedbackRequest', 'valid/feedback-request.json', true],
  ['GameplayMetricRequest', 'valid/gameplay-metric-request.json', true],
  ['PerformanceMetricRequest', 'valid/performance-metric-request.json', true],
  ['APIErrorResponse', 'valid/api-error-response.json', true],
  ['ReportResponse', 'valid/report-response.json', true],
];
for (const [schemaName, fixture, expected] of fixtureCases) {
  const value = JSON.parse(readFileSync(new URL(`protocol/fixtures/${fixture}`, root), 'utf8'));
  assert.equal(valid(document.components.schemas[schemaName], value).ok, expected, `${fixture} validation`);
}

for (const path of [
  '/api/v1/reports/crash',
  '/api/v1/reports/feedback',
  '/api/v1/metrics/gameplay',
  '/api/v1/metrics/performance',
]) {
  const response = resolveRef(document.paths[path].post.responses['426'].$ref);
  assert.deepEqual(
    Object.keys(response.content).sort(),
    ['application/json', 'text/plain'],
    `${path} must document both upgrade-required representations`,
  );
}
