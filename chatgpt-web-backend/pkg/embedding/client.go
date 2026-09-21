package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Client embedding 服务客户端
type Client struct {
	baseURL string
	http    *http.Client
}

// embedRequest Python /embed 接口请求体
type embedRequest struct {
	Text string `json:"text"`
}

// embedResponse Python /embed 接口响应体
type embedResponse struct {
	Embedding []float64 `json:"embedding"`
}

// rerankRequest Python /rerank 接口请求体
type rerankRequest struct {
	Query       string `json:"query"`
	CachedQuery string `json:"cached_query"`
}

// rerankResponse Python /rerank 接口响应体
type rerankResponse struct {
	Score float64 `json:"score"`
}

// NewClient 创建 embedding 客户端
// baseURL 如 "http://192.168.1.100:8001"
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Embed 将文本转为向量
func (c *Client) Embed(text string) ([]float64, error) {
	body, err := json.Marshal(embedRequest{Text: text})
	if err != nil {
		return nil, fmt.Errorf("embed marshal request: %w", err)
	}

	url := c.baseURL + "/embed"
	log.Printf("[Embed] 请求 URL: %s, text_len=%d", url, len(text))

	resp, err := c.http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[Embed] HTTP 请求失败: %v", err)
		return nil, fmt.Errorf("embed http post: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[Embed] 响应状态: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Printf("[Embed] 错误响应体: %s", string(b))
		return nil, fmt.Errorf("embed http status %d: %s", resp.StatusCode, string(b))
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[Embed] JSON 解析失败: %v", err)
		return nil, fmt.Errorf("embed decode response: %w", err)
	}

	log.Printf("[Embed] 成功, 向量维度=%d", len(result.Embedding))
	return result.Embedding, nil
}

// Rerank 对 query 和 cached_query 进行语义相似度重排打分
// 返回 0~1 之间的分数，越高越相似
func (c *Client) Rerank(query, cachedQuery string) (float64, error) {
	body, err := json.Marshal(rerankRequest{Query: query, CachedQuery: cachedQuery})
	if err != nil {
		return 0, fmt.Errorf("rerank marshal request: %w", err)
	}

	resp, err := c.http.Post(c.baseURL+"/rerank", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("rerank http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("rerank http status %d: %s", resp.StatusCode, string(b))
	}

	var result rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("rerank decode response: %w", err)
	}

	return result.Score, nil
}
