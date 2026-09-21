FROM python:3.10-slim AS builder
WORKDIR /build
ENV PIP_NO_CACHE_DIR=1

ADD ./torch-2.14.0+cpu-cp310-cp310-manylinux_2_28_x86_64.whl /build/
ADD ./requirements.txt /build/requirements.txt

RUN pip install -i https://pypi.tuna.tsinghua.edu.cn/simple --trusted-host pypi.tuna.tsinghua.edu.cn --upgrade pip && \
    pip install --prefix=/install \
    ./torch-2.14.0+cpu-cp310-cp310-manylinux_2_28_x86_64.whl \
    -r requirements.txt

FROM python:3.10-slim
WORKDIR /app
ENV PORT 8001
ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1

COPY --from=builder /install /usr/local
ADD ./main.py /app

CMD ["python", "main.py"]
