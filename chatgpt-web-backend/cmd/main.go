package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/Arvintian/chatgpt-web/pkg/controllers"
	"github.com/Arvintian/chatgpt-web/pkg/middlewares"
	"github.com/Arvintian/chatgpt-web/pkg/utils"
	"github.com/Arvintian/go-utils/cmdutil"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type ChatGPTWebServer struct {
	//字段名                类型    `键1:"值1" 键2:"值2" 键3:"值3"`字段附加一段文本注释，第三方库 / 标准库通过反射（reflect）读取，实现自动序列化、配置解析、参数绑定等功能。
	// 命令行参数 > 环境变量 > default 默认值 --host
	// 可以不传参数，没手动传值时，标签里 default:"xxx" 会自动填充默认值；
	// penAIBaseURL 默认值 https://api.deepseek.com 完全是靠结构体 tag 自动赋值；
	Host                   string `name:"host" env:"SERVER_HOST" usage:"http bind host" default:"0.0.0.0"`
	Port                   int    `name:"port" env:"SERVER_PORT" usage:"http bind port" default:"7080"`
	BasicAuthUser          string `name:"auth-user" env:"BASIC_AUTH_USER" usage:"http basic auth user"`
	BasicAuthPassword      string `name:"auth-password" env:"BASIC_AUTH_PASSWORD" usage:"http basic auth password"`
	FrontendPath           string `name:"frontend-path" env:"FRONTEND_PATH" default:"/app/public" usage:"frontend path"`
	SocksProxy             string `name:"socks-proxy" env:"SOCKS_PROXY" usage:"socks proxy url"`
	ChatSessionTTL         int    `name:"chat-session-ttl" env:"CHAT_SESSION_TTL" default:"30" usage:"chat session ttl minute"`
	ChatMinResponseTokens  int    `name:"chat-min-response-tokens" env:"CHAT_MIN_RESPONSE_TOKENS" default:"2048" usage:"chat min response tokens"`
	OpenAIKey              string `name:"openapi-key" env:"OPENAI_KEY" usage:"openai key"`
	OpenAIBaseURL          string `name:"openapi-base-url" env:"OPENAI_BASE_URL" default:"https://api.deepseek.com" usage:"openai base url"`
	OpenAIModel            string `name:"openai-model" env:"OPENAI_MODEL" default:"gpt-3.5-turbo-0301" usage:"openai params model"`
	OpenAIMaxTokens        int    `name:"openai-max-tokens" env:"OPENAI_MAX_TOKENS" default:"16384" usage:"openai params max-tokens"`
	OpenAITemperature      int    `name:"openai-temperature" env:"OPENAI_TEMPERATURE" default:"80" usage:"openai params temperature"`
	OpenAIPresencePenalty  int    `name:"openai-presence-penalty" env:"OPENAI_PRESENCE_PENALTY" default:"100" usage:"openai params presence-penalty"`
	OpenAIFrequencyPenalty int    `name:"openai-frequency-penalty" env:"OPENAI_FREQUENCY_PENALTY" default:"0" usage:"openai params frequency-penalty"`
	KVStoreMasterURL       string `name:"kv-store-master-url" env:"KV_STORE_MASTER_URL" default:"localhost:2000" usage:"external kv store master address for write (resp protocol)"`
	KVStoreSlaveURL        string `name:"kv-store-slave-url" env:"KV_STORE_SLAVE_URL" default:"localhost:2001" usage:"external kv store slave address for read (resp protocol)"`
	EmbeddingURL           string `name:"embedding-url" env:"EMBEDDING_URL" default:"http://localhost:8001" usage:"embedding service url"`
	Version                bool   `name:"version" usage:"show version"`
}

var Version = "0.0.0-dev"

func (r *ChatGPTWebServer) Run(cmd *cobra.Command, args []string) error {
	if r.Version {
		return r.ShowVersion()
	}
	gin.SetMode(gin.ReleaseMode)
	if err := r.updateAssetsFiles(); err != nil {
		return err
	}
	//go r.startTokenizer(cmd.Context()) // 这里去掉Tokenizer 我这里没有适配的环境
	go r.httpServer(cmd.Context())

	<-cmd.Context().Done()
	return nil
}

// 传参数 url key
func (r *ChatGPTWebServer) httpServer(ctx context.Context) {
	chatService, err := controllers.NewChatService(r.OpenAIKey, r.OpenAIBaseURL, controllers.ChatCompletionParams{
		Model:                 r.OpenAIModel,
		MaxTokens:             r.OpenAIMaxTokens,
		Temperature:           float32(r.OpenAITemperature) / 100.0,
		PresencePenalty:       float32(r.OpenAIPresencePenalty) / 100.0,
		FrequencyPenalty:      float32(r.OpenAIFrequencyPenalty) / 100.0,
		ChatMinResponseTokens: r.ChatMinResponseTokens,
		KVStoreMasterURL:      r.KVStoreMasterURL,
		KVStoreSlaveURL:       r.KVStoreSlaveURL,
		EmbeddingURL:          r.EmbeddingURL,
	})
	if err != nil {
		klog.Fatal(err)
	}

	addr := fmt.Sprintf("%s:%d", r.Host, r.Port)
	klog.Infof("ChatSylva Web Server on: %s", addr)
	server := &http.Server{
		Addr: addr,
	}
	entry, proxy := gin.New(), gin.New()
	entry.Use(gin.Logger())
	entry.Use(gin.Recovery())
	chat := entry.Group("/api")
	if len(r.BasicAuthUser) > 0 {
		accounts := gin.Accounts{}
		users := strings.Split(r.BasicAuthUser, ",")
		passwords := strings.Split(r.BasicAuthPassword, ",")
		if len(users) != len(passwords) {
			panic("basic auth setting error")
		}
		for i := 0; i < len(users); i++ {
			accounts[users[i]] = passwords[i]
		}
		chat.POST("/chat-process", gin.BasicAuth(accounts), middlewares.RateLimitMiddleware(1, 2), chatService.ChatProcess)
	} else {
		chat.POST("/chat-process", middlewares.RateLimitMiddleware(1, 2), chatService.ChatProcess)
	}

	chat.POST("/config", func(ctx *gin.Context) {
		ctx.JSON(200, gin.H{
			"status": "Success",
			"data": map[string]string{
				"apiModel":   "ChatGPTAPI",
				"socksProxy": r.SocksProxy,
			},
		})
	})

	chat.POST("/session", func(ctx *gin.Context) {
		ctx.JSON(200, gin.H{
			"status":  "Success",
			"message": "",
			"data": gin.H{
				"auth": false,
			},
		})
	})

	// 消息查询路由（从 KV 遍历 msg1..msgN）
	//if chatService.KVClient() != nil {
	//	msgCtrl := controllers.NewMsgController(chatService.KVClient())
	//chat.GET("/messages", msgCtrl.ListMessages)
	//chat.POST("/messages", msgCtrl.ListMessages)
	//klog.Infof("KV Store message API registered at /api/messages")
	//}
	upstreamURL, err := url.Parse(strings.TrimSuffix(r.OpenAIBaseURL, "/v1")) // upstreamURL, err := url.Parse(r.OpenAIBaseURL)
	if err != nil {
		klog.Fatal(err)
	}
	upstream := httputil.NewSingleHostReverseProxy(upstreamURL)
	if r.SocksProxy != "" {
		proxyUrl, err := url.Parse(r.SocksProxy)
		if err != nil {
			klog.Fatal(err)
		}
		upstream.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyUrl),
		}
	}
	apis := proxy.Group("/v1")
	apis.Any("/*relativePath", func(ctx *gin.Context) {
		ctx.Request.Host = upstreamURL.Host
		upstream.ServeHTTP(ctx.Writer, ctx.Request)
	})
	proxy.NoRoute(func(ctx *gin.Context) {
		http.FileServer(http.Dir(r.FrontendPath)).ServeHTTP(ctx.Writer, ctx.Request)
	})
	entry.NoRoute(func(ctx *gin.Context) {
		proxy.ServeHTTP(ctx.Writer, ctx.Request)
	})

	server.Handler = entry
	go func(ctx context.Context) {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown with error %v", err)
		}
	}(ctx)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server listen and serve error %v", err)
	}
}

func (r *ChatGPTWebServer) updateAssetsFiles() error {
	pairs := map[string]string{}
	old := `{avatar:"https://raw.githubusercontent.com/Chanzhaoyu/chatgpt-web/main/src/assets/avatar.jpg",name:"ChenZhaoYu",description:'Star on <a href="https://github.com/Chanzhaoyu/chatgpt-bot" class="text-blue-500" target="_blank" >Github</a>'}`
	new := `{avatar:"https://raw.githubusercontent.com/Chanzhaoyu/chatgpt-web/main/src/assets/avatar.jpg",name:"ChatGPT",description:'Star on <a href="https://github.com/Arvintian/chatgpt-web" class="text-blue-500" target="_blank" >Github</a>'}`
	pairs[old] = new
	old = `{}.VITE_GLOB_OPEN_LONG_REPLY`
	new = `{VITE_GLOB_OPEN_LONG_REPLY:"true"}.VITE_GLOB_OPEN_LONG_REPLY`
	pairs[old] = new
	old = `<link rel="manifest" href="/manifest.webmanifest"><script id="vite-plugin-pwa:register-sw" src="/registerSW.js"></script>`
	new = ``
	pairs[old] = new
	return utils.ReplaceFiles(r.FrontendPath, pairs)
}

func (r *ChatGPTWebServer) ShowVersion() error {
	fmt.Println(Version)
	return nil
}

func main() {
	// go-utils/cmdutil：独立开源工具库，负责 cobra 命令行、环境变量、default 标签自动赋值；
	root := cmdutil.Command(&ChatGPTWebServer{}, cobra.Command{
		Long: "ChatSylva Web Server",
	})
	//cmdutil 包利用 Go 反射 实现自动化
	cmdutil.Main(root)
}
