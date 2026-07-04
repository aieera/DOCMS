// Workspace templates (ADR 0118) — gallery CRUD + provision.
import { api } from './client'

export interface TemplateGrant {
  grantee_type: 'user' | 'group'
  grantee_id: string
}

export interface TemplateDoc {
  title: string
  metadata?: Record<string, string>
  tags?: string[]
}

export interface TemplateNode {
  name: string
  visibility?: 'shared' | 'private'
  metadata?: Record<string, string>
  grants?: TemplateGrant[]
  docs?: TemplateDoc[]
  children?: TemplateNode[]
}

export interface TemplateDefinition {
  nodes: TemplateNode[]
}

export interface WorkspaceTemplate {
  id: string
  name: string
  description: string
  definition: TemplateDefinition
  created_by: string
  created_at: string
  updated_at: string
}

export async function listTemplates(): Promise<WorkspaceTemplate[]> {
  const { data } = await api.get<{ templates: WorkspaceTemplate[] }>('/templates')
  return data.templates ?? []
}

export async function createTemplate(input: {
  name: string
  description?: string
  definition: TemplateDefinition
}): Promise<WorkspaceTemplate> {
  const { data } = await api.post<WorkspaceTemplate>('/templates', input)
  return data
}

export async function updateTemplate(id: string, input: {
  name: string
  description?: string
  definition: TemplateDefinition
}): Promise<WorkspaceTemplate> {
  const { data } = await api.put<WorkspaceTemplate>(`/templates/${id}`, input)
  return data
}

export async function deleteTemplate(id: string): Promise<void> {
  await api.delete(`/templates/${id}`)
}

export async function templateVariables(id: string): Promise<string[]> {
  const { data } = await api.get<{ variables: string[] }>(`/templates/${id}/variables`)
  return data.variables ?? []
}

export interface ProvisionResult {
  root_folder_ids: string[]
  folders_created: number
  docs_created: number
}

export async function provisionTemplate(id: string, input: {
  workspace_id: string
  parent_folder_id?: string
  variables?: Record<string, string>
}): Promise<ProvisionResult> {
  const { data } = await api.post<ProvisionResult>(`/templates/${id}/provision`, input)
  return data
}
