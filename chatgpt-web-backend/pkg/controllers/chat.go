package controllers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Arvintian/chatgpt-web/pkg/embedding"
	"github.com/Arvintian/chatgpt-web/pkg/kvstore"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openai "github.com/sashabaranov/go-openai"
	"k8s.io/klog/v2"
)

var globalTokens int
var golbalCutTokens int // 节省tokens

type ChatService struct {
	client      *openai.Client            // OpenAI 兼容的客户端（用于调用 API）
	params      ChatCompletionParams      // 模型参数配置
	kvClient    *kvstore.ExternalKVClient // KV 存储客户端（保存聊天记录）
	embedClient *embedding.Client         // Embedding 服务客户端（文本转向量）

	// 自定义流式请求所需字段（go-openai 不支持 thinking 字段，需手动发请求关闭 DeepSeek 思考模式）
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

type ChatCompletionParams struct {
	Model                 string  `json:"model"` //单次最大输出 token 上限
	MaxTokens             int     `json:"max_tokens,omitempty"`
	Temperature           float32 `json:"temperature,omitempty"`
	PresencePenalty       float32 `json:"presence_penalty,omitempty"`
	FrequencyPenalty      float32 `json:"frequency_penalty,omitempty"`
	ChatMinResponseTokens int     `json:"chat_min_response_tokens"`
	KVStoreMasterURL      string  `json:"kv_store_master_url"` // KV 主节点地址（写入）
	KVStoreSlaveURL       string  `json:"kv_store_slave_url"`  // KV 从节点地址（查询）
	EmbeddingURL          string  `json:"embedding_url"`       // Embedding 服务地址
}

type ChatMessageRequest struct {
	Prompt  string                    `json:"prompt"`  // 用户输入的文本
	Options ChatMessageRequestOptions `json:"options"` // 附加选项
}

type ChatMessageRequestOptions struct {
	Name            string `json:"name"`
	ParentMessageId string `json:"parentMessageId"`
	ConversationId  string `json:"conversationId"`
}

// KVClient 返回 KV Store 客户端（供外部路由注册使用）
func (chat *ChatService) KVClient() *kvstore.ExternalKVClient {
	return chat.kvClient
}

// 请求deepseek 地址 https://api.deepseek.com
func NewChatService(apiKey, baseURL string, params ChatCompletionParams) (*ChatService, error) {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL

	chat := &ChatService{
		client:     openai.NewClientWithConfig(config),
		params:     params,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: &http.Client{},
	}

	if params.KVStoreMasterURL != "" && params.KVStoreSlaveURL != "" {
		chat.kvClient = kvstore.NewExternalKVClient(params.KVStoreMasterURL, params.KVStoreSlaveURL)
		klog.Infof("Using external KV Store - master(write): %s, slave(read): %s", params.KVStoreMasterURL, params.KVStoreSlaveURL)
	} else if params.KVStoreMasterURL != "" {
		// 兼容：仅配置主节点时，读写都走主节点
		chat.kvClient = kvstore.NewExternalKVClient(params.KVStoreMasterURL, params.KVStoreMasterURL)
		klog.Infof("Using external KV Store (single): %s (both read & write)", params.KVStoreMasterURL)
	}

	if params.EmbeddingURL != "" {
		chat.embedClient = embedding.NewClient(params.EmbeddingURL)
		klog.Infof("Using embedding service: %s", params.EmbeddingURL)
	}

	return chat, nil
}

/*
 * 1. 收到web端消息： 写一个c语言版本红黑树
 * 2. 将中文转成向量 → VSEARCH → rerank → GET 缓存答案
 */
func (chat *ChatService) ChatProcess(ctx *gin.Context) {
	payload := ChatMessageRequest{}
	if err := ctx.BindJSON(&payload); err != nil {
		klog.Error(err)
		ctx.JSON(200, gin.H{
			"status":  "Fail",
			"message": fmt.Sprintf("%v", err),
			"data":    nil,
		})
		return
	}

	klog.Infof("收到web端消息: %s", payload.Prompt)

	// 语义缓存查找：用完整问题做向量检索（embedding → VSEARCH → rerank → GET）
	if chat.embedClient != nil && chat.kvClient != nil {
		if chat.trySemanticCache(ctx, payload, payload.Prompt) {
			return
		}
	}

	// 从 KV 读历史消息构建上下文
	// TODO: 这里暂时没有发送上下文的功能的
	messages, err := chat.buildMessages(payload)
	if err != nil {
		klog.Errorf("buildMessages error: %v", err)
		ctx.JSON(200, gin.H{
			"status":  "Fail",
			"message": fmt.Sprintf("build messages error: %v", err),
			"data":    nil,
		})
		return
	}

	// ✅ MaxTokens 修正：
	//  1) 原写法 `MaxTokens - len(payload.Prompt)` 用「prompt 字节数」去减「token 配额」单位不对，
	//     中文 1 字≈1~3 字节≈1 token，这会把可用 tokens 乱砍少甚至砍成负数。
	//  2) 对 reasoning 类模型（deepseek-v4-flash 等）更需要留足 tokens：思考 + 正式回答都要占配额。
	//  3) 结合 params.ChatMinResponseTokens 保证至少给回答留一个下限，避免被历史上下文挤没。
	completionMaxTokens := chat.params.MaxTokens
	if chat.params.ChatMinResponseTokens > 0 && completionMaxTokens < chat.params.ChatMinResponseTokens {
		completionMaxTokens = chat.params.ChatMinResponseTokens
	}
	if completionMaxTokens <= 0 {
		// deepseek-v4-flash 等推理模型的思考过程会消耗几千 tokens，
		// 4096 只够思考，不够输出正式回答，兜底默认值调大为 16384。
		completionMaxTokens = 16384
	}

	stream, err := chat.createStreamCompletion(ctx, openai.ChatCompletionRequest{
		Model:            chat.params.Model,
		Messages:         messages,
		MaxTokens:        completionMaxTokens,
		Temperature:      chat.params.Temperature,
		PresencePenalty:  chat.params.PresencePenalty,
		FrequencyPenalty: chat.params.FrequencyPenalty,
		TopP:             1,
		Stream:           true,
	}, true) // thinkingDisabled=true：关闭 DeepSeek 推理模型的思考模式，直接输出正式回答
	if err != nil {
		klog.Error(err)
		ctx.JSON(200, gin.H{
			"status":  "Fail",
			"message": fmt.Sprintf("%v", err),
			"data":    nil,
		})
		return
	}
	defer stream.Close()

	convId := payload.Options.ConversationId
	if convId == "" {
		convId = uuid.New().String()
	}
	// 请求大模型 CacheHit 为false 表示从大模型获取答案
	result := struct {
		ID              string                              `json:"id"`
		Text            string                              `json:"text"`
		Role            string                              `json:"role"`
		Delta           string                              `json:"delta"`
		Detail          openai.ChatCompletionStreamResponse `json:"detail"`
		ConversationId  string                              `json:"conversationId"`
		ParentMessageId string                              `json:"parentMessageId"`
		TotalTokens     int                                 `json:"totalTokens,omitempty"`
		CacheHit        bool                                `json:"cacheHit"`
		// Reasoning 预留字段（思考过程），后续加开关时再返回给前端，暂不序列化
		Reasoning string `json:"-"`
	}{
		ID:              uuid.New().String(),
		Role:            openai.ChatMessageRoleAssistant,
		ConversationId:  convId,
		ParentMessageId: uuid.New().String(),
		CacheHit:        false,
	}
	// 本次请求 completion / reasoning tokens 计数（用于 io.EOF 时判断能否用 reasoning 兜底）
	var (
		usageCompletionTokens int = -1
		usageReasoningTokens  int = -1
	)

	firstChunk := true
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.Header("X-Accel-Buffering", "no") // nginx accel 缓存关闭
	ctx.Header("Cache-Control", "no-cache")
	for {
		rsp, err := stream.Recv()

		if rsp.ID != "" {
			result.ID = rsp.ID
		}
		if len(rsp.Choices) > 0 {
			choice := rsp.Choices[0]
			content := choice.Delta.Content
			reasoning := ""

			// go-openai v1.41.2 的 ChatCompletionStreamChoiceDelta 没有暴露 ReasoningContent，
			// 但底层 JSON 里会有，所以从原始 JSON 里兜底提取 content / reasoning_content。
			if raw, err := json.Marshal(choice.Delta); err == nil {
				var rawDelta struct {
					ReasoningContent string `json:"reasoning_content"`
					Content          string `json:"content"`
				}
				if json.Unmarshal(raw, &rawDelta) == nil {
					if rawDelta.ReasoningContent != "" {
						reasoning = rawDelta.ReasoningContent
					}
					// 以 rawDelta.Content 为准（防止某些网关绕过 struct tag）
					if content == "" && rawDelta.Content != "" {
						content = rawDelta.Content
					}
				}
			}

			result.Reasoning += reasoning
			result.Delta = content
			if len(content) > 0 {
				result.Text += content
			}
			result.Detail = rsp

			// 🔍 诊断：content 与 reasoning 同时为空时打印完整 chunk JSON，
			// 帮助排查「扣了 token 但没 content」的真实字段。
			if content == "" && reasoning == "" {
				if raw, err := json.Marshal(rsp); err == nil {
					klog.Infof("🔍 chunk 全空原始JSON: %s", preview(string(raw), 500))
				}
			}

			// ⚠️ finish_reason = "length" 表示生成到 max_tokens 上限被硬截断，
			//    对 deepseek-v4-flash 这类 reasoning 模型，通常意味着「思考占满额度，正式回答没来得及生成」，
			//    需要调大 MaxTokens 或设置更大的 ChatMinResponseTokens。
			if rsp.Choices[0].FinishReason == "length" {
				klog.Warningf("⚠️ finish_reason=length（生成到 MaxTokens=%d 被截断），reasoning=%d 字, content=%d 字。"+
					" 若 content 为空请调大配置 max_tokens / chat_min_response_tokens",
					completionMaxTokens, len(result.Reasoning), len(result.Text))
			}

			if rsp.Choices[0].FinishReason != "" {

				raw, marshalErr := json.Marshal(rsp)

				var temp struct {
					Usage *struct {
						TotalTokens             int `json:"total_tokens"`
						CompletionTokens        int `json:"completion_tokens"`
						PromptTokens            int `json:"prompt_tokens"`
						CompletionTokensDetails *struct {
							ReasoningTokens int `json:"reasoning_tokens"`
						} `json:"completion_tokens_details"`
					} `json:"usage"`
					Choices []struct {
						// 有些供应商在 finish chunk 里把完整最终答案放到 message.content 里，
						// 而不是通过增量 delta 下发；如果流式累计的 result.Text 为空/偏短，
						// 就从这里兜底拿完整内容。
						Message *struct {
							Content          string `json:"content"`
							ReasoningContent string `json:"reasoning_content"`
						} `json:"message"`
					} `json:"choices"`
				}
				if marshalErr == nil {
					if json.Unmarshal(raw, &temp) == nil {
						if temp.Usage != nil {
							result.TotalTokens = temp.Usage.TotalTokens
							// 保存 completion / reasoning tokens，用于 io.EOF 时决策
							usageCompletionTokens = temp.Usage.CompletionTokens
							if temp.Usage.CompletionTokensDetails != nil {
								usageReasoningTokens = temp.Usage.CompletionTokensDetails.ReasoningTokens
							}
						} else {
							klog.Warning("⚠️ temp.Usage 是 nil，没有 usage 字段")
						}
						// ✅ 关键兜底：从 finish chunk 的 choices[0].message.content 提取完整回答
						if len(temp.Choices) > 0 && temp.Choices[0].Message != nil {
							fullContent := temp.Choices[0].Message.Content
							fullReasoning := temp.Choices[0].Message.ReasoningContent
							// 如果流式累计 result.Text 为空但 finish chunk 有完整 content，直接覆盖使用
							if len(result.Text) == 0 && len(fullContent) > 0 {
								klog.Infof("🔧 finish chunk 兜底：流式delta未取到content，使用message.content (len=%d)", len(fullContent))
								result.Text = fullContent
							} else if len(fullContent) > len(result.Text)+16 {
								// 如果完整 content 明显比流式累计更长，说明中间可能丢chunk，也兜底
								klog.Infof("🔧 finish chunk 兜底：流式累计len=%d，而message.content len=%d，使用更长的版本",
									len(result.Text), len(fullContent))
								result.Text = fullContent
							}
							// reasoning 同样兜底（仍只保留在预留字段，不返回前端）
							if len(fullReasoning) > len(result.Reasoning) {
								result.Reasoning = fullReasoning
							}
						}
					}
				}

				klog.Infof("本次对话原始Tokens:%v, completion=%d reasoning=%d, Text长度=%d, Reasoning长度=%d",
					result.TotalTokens, usageCompletionTokens, usageReasoningTokens,
					len(result.Text), len(result.Reasoning))

				// 🔍 finish_reason 到来时把完整 rsp JSON 写入 /tmp 文件，
				// klog 只打印预览 + 文件路径，避免单行超长被截断导致看起来「报文不全」。
				if marshalErr == nil {
					fp := dumpToTempFile("finish_chunk_"+convId[:8], ".json", raw)
					klog.Infof("🔍 finish chunk 原始JSON: preview=%s, 完整文件=%s",
						preview(string(raw), 300), fp)
				}
			}

		}

		if errors.Is(err, io.EOF) {
			// ================================================================
			// ⚠️ reasoning 兜底策略（更严格版）：
			//    只有当 completion 里确实包含至少 1 个 content token（reasoning_tokens < completion_tokens）
			//    才允许用 reasoning 做回答回退。
			//    如果 reasoning_tokens == completion_tokens（100% tokens 都在思考，一个 content 都没出），
			//    意味着「正式回答还没开始生成就被截断了」，reasoning 里全是自言自语不是答案，
			//    这种情况要 FAIL 并提示必须调大 max_tokens，而不是把思考过程塞给用户/入库。
			// ================================================================
			reasoningAllPureThinking := false
			if usageCompletionTokens > 0 && usageReasoningTokens >= usageCompletionTokens {
				// 100% completion tokens 全是 reasoning，一个正式 content token 都没出
				reasoningAllPureThinking = true
			}

			if len(result.Text) == 0 && len(result.Reasoning) > 0 && !reasoningAllPureThinking {
				klog.Warningf("🔧 回退：content为空(%d字)但reasoning非空(%d字)，completion=%d reasoning=%d（有部分content tokens），"+
					"将reasoning用作正式回答。后续考虑调大 max_tokens / chat_min_response_tokens 以获取正常 content",
					len(result.Text), len(result.Reasoning), usageCompletionTokens, usageReasoningTokens)
				result.Text = result.Reasoning
				result.Delta = result.Reasoning
			} else if len(result.Text) == 0 && len(result.Reasoning) > 0 && reasoningAllPureThinking {
				klog.Errorf("❌ 拒绝用 reasoning 兜底：completion=%d tokens 全是 reasoning(%d)，一个正式content token都没有。"+
					" reasoning里是纯思考过程不是答案！必须调大配置 max_tokens（建议≥16384）并增大 chat_min_response_tokens，"+
					" 让模型思考完还有剩余 tokens 输出正式回答。 Reasoning预览: %s",
					usageCompletionTokens, usageReasoningTokens, preview(result.Reasoning, 200))
			}

			// ✅ 关键修复：只有大模型返回了有效非空内容，才写入 KV / 向量库 / 累计 tokens
			if len(result.Text) == 0 {
				klog.Errorf("⚠️ 大模型返回空内容（result.Text为空，Reasoning无法兜底），不写入KV Store和向量库。问题: %s。"+
					" 建议：配置文件中将 max_tokens 调大为 16384 以上，并设置 chat_min_response_tokens ≥ 2048，"+
					" 确保 reasoning 模型思考完还有额度输出正式回答。", payload.Prompt)
				// 向前端返回一个错误标识的最终 chunk
				failResult := result
				failResult.Delta = ""
				failResult.Text = ""
				failResult.CacheHit = false
				if bts, e := json.Marshal(failResult); e == nil {
					if !firstChunk {
						ctx.Writer.Write([]byte("\n"))
					}
					ctx.Writer.Write(bts)
					ctx.Writer.Flush()
				}
				return
			}

			// content 非空，说明请求成功：累计 tokens、写入 KV、写入向量库
			if result.TotalTokens > 0 {
				globalTokens += result.TotalTokens
				result.TotalTokens = globalTokens
				klog.Infof("本次对话消耗Tokens(累计后):%v", result.TotalTokens)
				klog.Infof("总共消耗 Total Tokens: %d", globalTokens)

				// 本次对话token消耗存储到kvstore中
				tokenNum := strconv.Itoa(result.TotalTokens)
				prefix_key := addPrefix(payload.Prompt)
				klog.Infof("tokenum:%s, key:%s", tokenNum, prefix_key)

				if chat.kvClient != nil {
					if err := chat.kvClient.PutMsg(prefix_key, tokenNum); err != nil {
						klog.Errorf("KV PutMsg(token) failed: %v", err)
					}
				}
			}

			if bts, e := json.Marshal(result); e == nil {
				if !firstChunk {
					ctx.Writer.Write([]byte("\n"))
				}
				ctx.Writer.Write(bts)
				ctx.Writer.Flush()
			}

			// 响应内容可能是几千行代码，klog 单行打印会被截断、换行也会冲乱日志，
			// 改为：只打印长度+前200字预览，完整内容写到 /txt 文件方便查看。
			// fp := dumpToTempFile("response_"+convId[:8], ".txt", []byte(result.Text))

			// klog.Infof("响应消息: len=%d, preview=%s, 完整文件=%s", len(result.Text), preview(result.Text, 200), fp)

			// 存入 KV: 用户输入 key, AI响应 value
			if chat.kvClient != nil {
				if err := chat.kvClient.PutMsg(payload.Prompt, result.Text); err != nil {
					klog.Errorf("KV PutMsg(user) failed: %v", err)
				}
			}

			// 将问题向量存入向量库（VADD），供后续语义缓存命中
			if chat.embedClient != nil && chat.kvClient != nil {
				vec, err := chat.embedClient.Embed(payload.Prompt)
				if err != nil {
					klog.Warningf("VADD: embed 失败: %v", err)
				} else if err := chat.kvClient.VAdd(payload.Prompt, vec); err != nil {
					klog.Warningf("VADD: 失败: %v", err)
				} else {
					klog.Infof("VADD: 已缓存向量 key=%s", payload.Prompt)
				}
			}

			return
		}

		if err != nil {
			klog.Error(err)
			ctx.JSON(200, gin.H{
				"status":  "Fail",
				"message": fmt.Sprintf("OpenAI Event Error %v", err),
				"data":    nil,
			})
			return
		}

		bts, err := json.Marshal(result)
		if err != nil {
			klog.Error(err)
			return
		}

		if !firstChunk {
			ctx.Writer.Write([]byte("\n"))
		} else {
			firstChunk = false
		}

		if _, err := ctx.Writer.Write(bts); err != nil {
			klog.Error(err)
			return
		}
		ctx.Writer.Flush()
	}
}

// buildMessages 从 KV 读 msg1..msgN 构建 OpenAI 消息列表
func (chat *ChatService) buildMessages(payload ChatMessageRequest) ([]openai.ChatCompletionMessage, error) {
	messages := make([]openai.ChatCompletionMessage, 0)

	// 废弃 GetMsg 接口
	// 如果连了 KV，读历史消息当作上下文
	// if chat.kvClient != nil {
	// 	for i := 1; ; i++ {
	// 		text, ok, err := chat.kvClient.GetMsg(i)
	// 		if err != nil {
	// 			klog.Errorf("KV GetMsg(%d) error: %v", i, err)
	// 			break
	// 		}
	// 		if !ok {
	// 			break
	// 		}

	// 		role := openai.ChatMessageRoleUser
	// 		if i%2 == 0 {
	// 			role = openai.ChatMessageRoleAssistant
	// 		}
	// 		messages = append(messages, openai.ChatCompletionMessage{
	// 			Role:    role,
	// 			Content: text,
	// 		})
	// 	}
	// }

	// 追加当前用户输入
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: payload.Prompt,
		Name:    payload.Options.Name,
	})

	return messages, nil
}

// trySemanticCache 语义缓存查找：embed → VSEARCH → rerank → GET 缓存答案
// searchText 即完整原问题，用于向量检索与 rerank 精排。
// 返回 true 表示命中缓存并已返回结果，返回 false 表示未命中需继续走 LLM
func (chat *ChatService) trySemanticCache(ctx *gin.Context, payload ChatMessageRequest, searchText string) bool {
	const similarityThreshold = 0.7 // rerank 分数阈值，低于此值视为不相似

	// Step 1: 中文文本转向量（用完整原问题）
	vec, err := chat.embedClient.Embed(searchText)
	if err != nil {
		klog.Warningf("语义缓存: embed 失败: %v", err)
		return false
	}
	klog.Info("Step 1: 中文文本转向量")
	klog.Infof("检索文本[%v]", searchText)

	// Step 2: VSEARCH 向量搜索 top-5
	keys, err := chat.kvClient.VSearch(vec, 5)
	if err != nil {
		klog.Warningf("语义缓存: VSEARCH 失败: %v", err)
		return false
	}
	if len(keys) == 0 {
		klog.Infof("语义缓存: VSEARCH 无结果")
		return false
	}

	// Step 3: rerank 精排，取最高分
	var bestKey string
	var bestScore float64
	for _, key := range keys {
		score, err := chat.embedClient.Rerank(payload.Prompt, key)
		if err != nil {
			klog.Warningf("语义缓存: rerank(%s) 失败: %v", key, err)
			continue
		}
		klog.Infof("语义缓存: rerank(query=%s, cached=%s) score=%.4f", payload.Prompt, key, score)
		if score > bestScore {
			bestScore = score
			bestKey = key
		}
	}

	if bestScore < similarityThreshold {
		klog.Infof("语义缓存: rerank 最高分 %.4f < %.2f，未命中", bestScore, similarityThreshold)
		return false
	}

	// Step 4: 从 KV Store 获取缓存答案
	answer, ok, err := chat.kvClient.Get(bestKey)
	if err != nil || !ok {
		klog.Warningf("语义缓存: GET(%s) 失败: err=%v, ok=%v", bestKey, err, ok)
		return false
	}

	klog.Infof("语义缓存: 命中! key=%s, score=%.4f, answer_len=%d", bestKey, bestScore, len(answer))
	// 给返回增加一个标签,表示在本地缓存中CacheHit 等于true,前端判断这个标签 进行标注来自大模型还是本地存储

	// 从kvsore 查询节省的token数量,并计算到总值中
	// tokenNum := strconv.Itoa(result.TotalTokens)
	prefix_key := addPrefix(bestKey)
	klog.Infof("key:%s", prefix_key)

	text, _, err := chat.kvClient.Get(prefix_key)
	if err != nil {
		klog.Errorf("KV GetMsg(%v) error: %v", prefix_key, err)
	}
	klog.Infof("本次对话语义缓存token : %s", text)

	num, err := strconv.Atoi(text)
	if err != nil {
		klog.Errorf("转整数失败:%s", err)
	}
	golbalCutTokens += num

	// Step 5: 以 SSE 流式格式返回缓存答案
	convId := payload.Options.ConversationId
	if convId == "" {
		convId = uuid.New().String()
	}

	result := struct {
		ID              string                              `json:"id"`
		Text            string                              `json:"text"`
		Role            string                              `json:"role"`
		Delta           string                              `json:"delta"`
		Detail          openai.ChatCompletionStreamResponse `json:"detail"`
		ConversationId  string                              `json:"conversationId"`
		ParentMessageId string                              `json:"parentMessageId"`
		TotalTokens     int                                 `json:"totalTokens,omitempty"`
		CutTokens       int                                 `json:"CutTokens"`
		CacheHit        bool                                `json:"cacheHit"`
	}{
		ID:    uuid.New().String(),
		Text:  answer,
		Role:  openai.ChatMessageRoleAssistant,
		Delta: answer,
		Detail: openai.ChatCompletionStreamResponse{
			Choices: []openai.ChatCompletionStreamChoice{
				{FinishReason: "stop"},
			},
		},
		ConversationId:  convId,
		ParentMessageId: uuid.New().String(),
		CutTokens:       golbalCutTokens,
		CacheHit:        true,
	}

	ctx.Header("Content-type", "application/octet-stream")
	ctx.Header("X-Accel-Buffering", "no")
	bts, _ := json.Marshal(result)
	ctx.Writer.Write(bts)
	ctx.Writer.Flush()

	return true
}

func addPrefix(input string) string {
	const prefix = "ds_tknum:"
	return prefix + input
}

// dumpToTempFile 把大块内容写入 /tmp 下的临时文件，避免 klog 单行打印被截断。
// 返回文件路径用于日志里提示。
func dumpToTempFile(tag, suffix string, data []byte) string {
	name := fmt.Sprintf("/tmp/chatdbg_%s_%d_%s%s",
		tag, time.Now().UnixNano(), uuid.New().String()[:8], suffix)
	if err := os.WriteFile(name, data, 0644); err != nil {
		klog.Warningf("dumpToTempFile 写入失败 %s: %v", name, err)
		return ""
	}
	return name
}

// preview 取字符串前 n 个字符做预览（用于日志，避免打印超长）。
func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(共%d字)", len(s))
}

// sseStream 自定义 SSE 流读取器，替代 go-openai 的 ChatCompletionStream。
// 目的：go-openai v1.41.2 的 ChatCompletionRequest 结构体没有 thinking 字段，
// 无法关闭 DeepSeek V4 推理模型的思考模式，因此手动构造请求并解析 SSE。
type sseStream struct {
	reader *bufio.Reader
	closer io.Closer
}

// Recv 读取下一个 SSE data 块并解析为 ChatCompletionStreamResponse。
// 返回 io.EOF 表示流正常结束（收到 [DONE] 或底层 body 读完）。
func (s *sseStream) Recv() (openai.ChatCompletionStreamResponse, error) {
	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return openai.ChatCompletionStreamResponse{}, io.EOF
			}
			return openai.ChatCompletionStreamResponse{}, err
		}
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if string(data) == "[DONE]" {
			return openai.ChatCompletionStreamResponse{}, io.EOF
		}
		var rsp openai.ChatCompletionStreamResponse
		if err := json.Unmarshal(data, &rsp); err != nil {
			// 无法解析的行（如 keep-alive 注释）直接跳过
			continue
		}
		return rsp, nil
	}
}

// Close 关闭底层响应 body。
func (s *sseStream) Close() error {
	return s.closer.Close()
}

// createStreamCompletion 自定义流式请求。
// thinkingDisabled=true 时给请求体注入 {"thinking":{"type":"disabled"}}，
// 关闭 DeepSeek V4 推理模型的思考模式，让模型直接输出 content（正式回答），
// 避免思考过程耗尽 max_tokens 导致 content 为空。
// 参考官方文档：https://api-docs.deepseek.com/guides/thinking_mode
func (chat *ChatService) createStreamCompletion(ctx context.Context, req openai.ChatCompletionRequest, thinkingDisabled bool) (*sseStream, error) {
	req.Stream = true
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// 注入 thinking 字段（DeepSeek 官方关闭思考的参数，注意不能传 reasoning_effort:"none"，会 400）
	if thinkingDisabled {
		var bodyMap map[string]any
		if err := json.Unmarshal(bodyBytes, &bodyMap); err != nil {
			return nil, err
		}
		bodyMap["thinking"] = map[string]string{"type": "disabled"}
		bodyBytes, err = json.Marshal(bodyMap)
		if err != nil {
			return nil, err
		}
	}

	url := strings.TrimRight(chat.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+chat.apiKey)

	resp, err := chat.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		bts, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("上游 API 返回 %d: %s", resp.StatusCode, string(bts))
	}

	return &sseStream{
		reader: bufio.NewReader(resp.Body),
		closer: resp.Body,
	}, nil
}
