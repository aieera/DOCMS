import { api } from './client'

// grpc-gateway serialises google.protobuf.Struct as a plain JSON
// object. The response shape is { json_schema: {...} } whether the
// object is empty or populated.
interface SchemaResponse {
  json_schema?: Record<string, unknown>
}

export async function getMetadataSchema(): Promise<Record<string, unknown>> {
  const { data } = await api.get<SchemaResponse>('/tenants/metadata-schema')
  return data.json_schema ?? {}
}

export async function updateMetadataSchema(
  jsonSchema: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const { data } = await api.put<SchemaResponse>('/tenants/metadata-schema', {
    json_schema: jsonSchema,
  })
  return data.json_schema ?? {}
}
