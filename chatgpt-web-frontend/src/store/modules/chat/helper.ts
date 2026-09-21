export function defaultState(): Chat.ChatState {
  const uuid = 1002
  return {
    active: uuid,
    usingContext: true,
    history: [{ uuid, title: 'New Chat', isEdit: false }],
    chat: [{ uuid, data: [] }],
    totalTokens: 0,
    savedTokens: 0,
  }
}

export function getLocalState(): Chat.ChatState {
  return defaultState()
}
