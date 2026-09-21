#!/bin/sh

#本地没有从网路中下载
#

src_file="$HOME/torch-2.14.0+cpu-cp310-cp310-manylinux_2_28_x86_64.whl"
if [ -f "$src_file" ]; then
    cp "$src_file" ./chatgpt-embedding/. && echo "cp 本地文件成功" || echo "cp 本地文件失败"
else
    echo "源文件不存在，跳过拷贝"
fi

docker-compose up -d
