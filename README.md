
### TODO
```
1.前端采用左边栏描述历史对话的问题, 上下关联本次对话的所有信息 []
2.把token和历史信息存储到数据库中
3.最好把ip地址修改成环境变量
4.实现一个返回流相应前端自动向下滑动的效果最好可关闭可打开的[V]
5.前端打包好了如何放到后端一块启动,已经改成了前端放到nginx中启动[V]
6.把本地存储到数据库中每次加载从数据库到redis中查询
```

### 第二修改
```
1. 主从同步测试 [V]
2. 名字修改前端web 为ChatSylva
```


### 第三次修改
```
1.增加本地关键词库,未采用grpc,通过测试关键词不能解决向量不匹配问题
2.使用frpc导致流处理缓存到中间nginx中[X],目前不知道怎么弄?
```




### init
```
docker rm -f kvstore chatgpt-go-backend chatgpt-web
docker network create chat-network
```


### backend

```
export GOPROXY=https://goproxy.cn,direct
CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -o dist/server ./cmd/main.go
docker rm -f chatgpt-go-backend 2>/dev/null
docker build -t chatgpt-go-backend .
docker run -d --name chatgpt-go-backend --network chat-network -p 7080:7080 --restart unless-stopped   chatgpt-go-backend:latest
docker logs -f chatgpt-go-backend
```

chatgpt-go-backend:latest 

### frontend
```
pnpm install
pnpm run build-only
docker build -t chatgpt-web-frontend .
docker run -d -p 8080:80 --name chatgpt-web chatgpt-web-frontend
```

### 测试
http://10.211.55.7:8080/

# ChatSylva
