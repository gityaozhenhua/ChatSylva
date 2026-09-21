import type { AxiosProgressEvent, GenericAbortSignal } from 'axios'
import { get, post } from '@/utils/request'

// ---------- 聊天 ----------

export function fetchChatAPIProcess<T = any>(
  params: {
    prompt: string
    options?: { conversationId?: string; parentMessageId?: string }
    signal?: GenericAbortSignal
    onDownloadProgress?: (progressEvent: AxiosProgressEvent) => void },
) {
  return post<T>({
    url: '/chat-process',
    data: {
      prompt: params.prompt,
      options: params.options,
    },
    signal: params.signal,
    responseType: 'text',
    onDownloadProgress: params.onDownloadProgress,
  })
}

export function fetchChatConfig<T = any>() {
  return post<T>({
    url: '/config',
  })
}

export function fetchChatSession<T>() {
  return post<T>({
    url: '/session',
  })
}

export function fetchVerify<T>(token: string) {
  return post<T>({
    url: '/verify',
    data: { token },
  })
}

// ---------- 消息历史（从 KV 遍历 msg1..msgN）----------

export interface MessageItem {
  role: 'user' | 'assistant'
  content: string
}

export async function fetchMessages(): Promise<MessageItem[]> {
  const res = await get<{ messages: MessageItem[] }>({
    url: '/messages',
  })
  return res.data.messages || []
}
