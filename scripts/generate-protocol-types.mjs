import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = new URL('../', import.meta.url);
const schemaPath = new URL('protocol/openapi.json', root);
const outputPath = new URL('protocol/index.d.ts', root);
const document = JSON.parse(readFileSync(schemaPath, 'utf8'));

assert.equal(document.openapi, '3.1.0', 'protocol schema must use OpenAPI 3.1.0');
assert(document.components?.schemas, 'protocol schema must define components.schemas');

const refName = ref => {
  const prefix = '#/components/schemas/';
  assert(ref.startsWith(prefix), `unsupported reference: ${ref}`);
  return ref.slice(prefix.length);
};

const literal = value => (typeof value === 'string' ? JSON.stringify(value) : String(value));

const renderType = schema => {
  if (schema === false) return 'never';
  if (schema.$ref) return refName(schema.$ref);
  if (schema.const !== undefined) return literal(schema.const);
  if (schema.enum) return schema.enum.map(literal).join(' | ');
  if (schema.oneOf) return schema.oneOf.map(renderType).join(' | ');
  if (schema.allOf) return schema.allOf.map(renderType).join(' & ');
  if (Array.isArray(schema.type)) return schema.type.map(type => renderType({ ...schema, type })).join(' | ');
  switch (schema.type) {
    case 'string':
      return 'string';
    case 'integer':
    case 'number':
      return 'number';
    case 'boolean':
      return 'boolean';
    case 'array':
      return `${renderNested(schema.items)}[]`;
    case 'object':
    case undefined:
      return renderObject(schema);
    default:
      throw new Error(`unsupported schema type: ${schema.type}`);
  }
};

const renderNested = schema => {
  const rendered = renderType(schema);
  return schema.oneOf || schema.allOf ? `(${rendered})` : rendered;
};

const renderObject = schema => {
  if (schema.additionalProperties && !schema.properties) {
    return `Record<string, ${renderType(schema.additionalProperties)}>`;
  }
  const required = new Set(schema.required ?? []);
  const fields = Object.entries(schema.properties ?? {}).map(([name, property]) => {
    const optional = required.has(name) ? '' : '?';
    return `  ${name}${optional}: ${renderType(property)};`;
  });
  if (schema.additionalProperties && schema.additionalProperties !== false) {
    fields.push(`  [key: string]: ${renderType(schema.additionalProperties)};`);
  }
  return `\{\n${fields.join('\n')}\n\}`;
};

const declarations = Object.entries(document.components.schemas).map(
  ([name, schema]) => `export type ${name} = ${renderType(schema)};`,
);
const output = `/** Generated from protocol/openapi.json. Do not edit directly. */\n\n${declarations.join('\n\n')}\n`;

if (process.argv.includes('--check')) {
  assert.equal(readFileSync(outputPath, 'utf8'), output, 'protocol/index.d.ts is stale; run npm run generate');
} else {
  writeFileSync(fileURLToPath(outputPath), output);
}
