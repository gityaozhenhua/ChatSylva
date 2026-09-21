FROM quay.io/0voice/alpine:3.18
WORKDIR /app
COPY config.yaml ./
COPY frp_0.52.2_linux_amd64.tar.gz frp.tar.gz
RUN tar -zxvf frp.tar.gz -C ./ --strip-components=1
RUN rm frp.tar.gz
ENTRYPOINT ["./frpc"]
CMD ["-c", "config.yaml"]
