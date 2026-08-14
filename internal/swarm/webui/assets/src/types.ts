export interface EventPayload {
  id: string
  type: string
  worker_id: string
  target_worker_id: string
  timestamp: number
  trace_id: string
  recipients?: string[]
  payload: Record<string, any>
}

export interface WorkerInfo {
  id: string
  type: string
  credential?: string
  publish_allow?: string[]
  subscribe_allow?: string[]
  online?: boolean
  managed?: boolean
  state?: string // "running" | "suspended" (managed only)
}
