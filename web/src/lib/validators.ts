import { z } from 'zod'

export const loginSchema = z.object({
  email: z.string().email('Invalid email'),
  password: z.string().min(8, 'Password must be at least 8 characters'),
})

export const registerSchema = z.object({
  email: z.string().email('Invalid email'),
  password: z.string().min(8).regex(/[A-Z]/, 'Must contain uppercase').regex(/[0-9]/, 'Must contain number'),
  display_name: z.string().min(2, 'Name too short').max(100),
})

export const createWorkspaceSchema = z.object({
  name: z.string().min(1, 'Required').max(100),
  description: z.string().max(500).optional(),
})

export const createFolderSchema = z.object({
  name: z.string().min(1, 'Required').max(255),
})

export const savedSearchSchema = z.object({
  name: z.string().min(1, 'Required').max(100),
  query: z.string().min(1, 'Required'),
  notify: z.boolean().default(false),
  notify_interval_minutes: z.number().min(5).default(15),
})

export const inviteUserSchema = z.object({
  email: z.string().email('Invalid email'),
  role: z.enum(['admin', 'member', 'guest']),
})

export const webhookSchema = z.object({
  url: z.string().url('Invalid URL'),
  events: z.array(z.string()).min(1, 'Select at least one event'),
})
