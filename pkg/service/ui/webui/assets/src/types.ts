export interface EventPayload {
  id: string
  type: string
  worker_id: string
  target_worker_id: string
  timestamp: number
  trace_id: string
  payload: Record<string, any>
}

export interface Decision {
  request_id: string
  worker_id: string
  summary: string
  context: string
  options: { id: string; label: string }[]
  trace_id: string
  status: string
  created_at: number
  decision?: string
  reasoning?: string
}

export interface WorkerInfo {
  id: string
  type: string
}
