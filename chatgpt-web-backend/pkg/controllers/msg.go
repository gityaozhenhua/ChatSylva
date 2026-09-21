package controllers

import (
	"github.com/Arvintian/chatgpt-web/pkg/kvstore"
)

// MsgController 消息查询控制器
type MsgController struct {
	kv *kvstore.ExternalKVClient
}

func NewMsgController(kv *kvstore.ExternalKVClient) *MsgController {
	return &MsgController{kv: kv}
}

// ListMessages 前端调用：遍历 msg1,msg2... 直到找不到，偶数=AI 奇数=用户
// GET/POST /api/messages
/*
func (c *MsgController) ListMessages(ctx *gin.Context) {
	var result []gin.H

	for i := 1; ; i++ {
		text, ok, err := c.kv.GetMsg(i)
		if err != nil {
			klog.Errorf("KV GetMsg(%d) error: %v", i, err)
			break
		}
		if !ok {
			break
		}

		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}

		result = append(result, gin.H{
			"role":    role,
			"content": text,
		})
	}

	ctx.JSON(200, gin.H{"status": "Success", "data": gin.H{"messages": result}})
}*/
