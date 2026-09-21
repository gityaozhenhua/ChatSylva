
pip3 install -r requirements.txt

配置 pip 国内源（安装依赖用）
临时生效（当前终端）
pip config set global.index-url https://pypi.tuna.tsinghua.edu.cn/simple



// other  method
echo '[global]
index-url = https://pypi.tuna.tsinghua.edu.cn/simple' >> ~/.pip/pip.conf

设置 HF 国内镜像地址
预加载两个大模型（进程启动时一次性载入内存）
BAAI/bge-small-zh-v1.5：中文向量模型（Embedding）
BAAI/bge-reranker-v2-m3：中文重排序打分模型（Rerank）
