import { defineStore } from 'pinia'
import { getLocalState } from './helper'
import { router } from '@/router'
import { fetchMessages } from '@/api'

export const useChatStore = defineStore('chat-store', {
  state: (): Chat.ChatState => getLocalState(),

  getters: {
    getChatHistoryByCurrentActive(state: Chat.ChatState) {
      const index = state.history.findIndex(item => item.uuid === state.active)
      if (index !== -1)
        return state.history[index]
      return null
    },

    getChatByUuid(state: Chat.ChatState) {
      return (uuid?: number) => {
        if (uuid)
          return state.chat.find(item => item.uuid === uuid)?.data ?? []
        return state.chat.find(item => item.uuid === state.active)?.data ?? []
      }
    },
  },

  actions: {
    // 从后端拉取消息历史，填充到当前活跃会话
    async loadMessages() {
      try {
        const msgs = await fetchMessages()
        if (msgs.length === 0)
          return

        const uuid = this.active ?? 1002
        const chatIndex = this.chat.findIndex(item => item.uuid === uuid)
        const chats: Chat.Chat[] = msgs.map((m, i) => ({
          dateTime: '',
          text: m.content,
          inversion: m.role === 'user',
          error: false,
          loading: false,
          conversationOptions: null,
          requestOptions: { prompt: m.content, options: null },
        }))

        if (chatIndex !== -1) {
          this.chat[chatIndex].data = chats
        }
        else {
          this.chat.unshift({ uuid, data: chats })
        }
      }
      catch {
        // 后端不可用，忽略
      }
    },

    setUsingContext(context: boolean) {
      this.usingContext = context
    },

    addHistory(history: Chat.History, chatData: Chat.Chat[] = []) {
      this.history.unshift(history)
      this.chat.unshift({ uuid: history.uuid, data: chatData })
      this.active = history.uuid
      this.reloadRoute(history.uuid)
    },

    updateHistory(uuid: number, edit: Partial<Chat.History>) {
      const index = this.history.findIndex(item => item.uuid === uuid)
      if (index !== -1)
        this.history[index] = { ...this.history[index], ...edit }
    },

    async deleteHistory(index: number) {
      this.history.splice(index, 1)
      this.chat.splice(index, 1)

      if (this.history.length === 0) {
        this.active = null
        this.reloadRoute()
        return
      }

      if (index > 0 && index <= this.history.length) {
        const uuid = this.history[index - 1].uuid
        this.active = uuid
        this.reloadRoute(uuid)
        return
      }

      if (index === 0) {
        if (this.history.length > 0) {
          const uuid = this.history[0].uuid
          this.active = uuid
          this.reloadRoute(uuid)
        }
      }

      if (index > this.history.length) {
        const uuid = this.history[this.history.length - 1].uuid
        this.active = uuid
        this.reloadRoute(uuid)
      }
    },

    async setActive(uuid: number) {
      this.active = uuid
      return await this.reloadRoute(uuid)
    },

    getChatByUuidAndIndex(uuid: number, index: number) {
      if (!uuid || uuid === 0) {
        if (this.chat.length)
          return this.chat[0].data[index]
        return null
      }
      const chatIndex = this.chat.findIndex(item => item.uuid === uuid)
      if (chatIndex !== -1)
        return this.chat[chatIndex].data[index]
      return null
    },

    addChatByUuid(uuid: number, chat: Chat.Chat) {
      if (!uuid || uuid === 0) {
        if (this.history.length === 0) {
          const uuid = Date.now()
          this.history.push({ uuid, title: chat.text, isEdit: false })
          this.chat.push({ uuid, data: [chat] })
          this.active = uuid
        }
        else {
          this.chat[0].data.push(chat)
          if (this.history[0].title === 'New Chat')
            this.history[0].title = chat.text
        }
      }

      const index = this.chat.findIndex(item => item.uuid === uuid)
      if (index !== -1) {
        this.chat[index].data.push(chat)
        if (this.history[index].title === 'New Chat')
          this.history[index].title = chat.text
      }
    },

    updateChatByUuid(uuid: number, index: number, chat: Chat.Chat) {
      if (!uuid || uuid === 0) {
        if (this.chat.length)
          this.chat[0].data[index] = chat
        return
      }

      const chatIndex = this.chat.findIndex(item => item.uuid === uuid)
      if (chatIndex !== -1)
        this.chat[chatIndex].data[index] = chat
    },

    updateChatSomeByUuid(uuid: number, index: number, chat: Partial<Chat.Chat>) {
      if (!uuid || uuid === 0) {
        if (this.chat.length)
          this.chat[0].data[index] = { ...this.chat[0].data[index], ...chat }
        return
      }

      const chatIndex = this.chat.findIndex(item => item.uuid === uuid)
      if (chatIndex !== -1)
        this.chat[chatIndex].data[index] = { ...this.chat[chatIndex].data[index], ...chat }
    },

    deleteChatByUuid(uuid: number, index: number) {
      if (!uuid || uuid === 0) {
        if (this.chat.length)
          this.chat[0].data.splice(index, 1)
        return
      }

      const chatIndex = this.chat.findIndex(item => item.uuid === uuid)
      if (chatIndex !== -1)
        this.chat[chatIndex].data.splice(index, 1)
    },

    clearChatByUuid(uuid: number) {
      if (!uuid || uuid === 0) {
        if (this.chat.length)
          this.chat[0].data = []
        return
      }

      const index = this.chat.findIndex(item => item.uuid === uuid)
      if (index !== -1)
        this.chat[index].data = []
    },

    async reloadRoute(uuid?: number) {
      await router.push({ name: 'Chat', params: { uuid } })
    },

    updateTotalTokens(tokens: number) {
      this.totalTokens = tokens
    },
    updateSavedTokens(tokens: number) {
      this.savedTokens = tokens
    },
  },
})
