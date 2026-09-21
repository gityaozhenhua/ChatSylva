package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type ExternalKVClient struct {
	masterAddr string
}

// SearchItem VSEARCH相似度检索返回项
type SearchItem struct {
	Key      string
	Distance float64
}

// VAdd 存储向量到 KV Store: key=问题原文, value=逗号分隔浮点数向量（写入主节点）
func (c *ExternalKVClient) VAdd(key string, vector []float64) error {
	vecStr := vecToStr(vector)

	var buf bytes.Buffer
	buf.WriteString("*3\r\n$4\r\nVADD\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(key)))
	buf.WriteString("\r\n")
	buf.WriteString(key)
	buf.WriteString("\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(vecStr)))
	buf.WriteString("\r\n")
	buf.WriteString(vecStr)
	buf.WriteString("\r\n")

	conn, err := net.DialTimeout("tcp", c.masterAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("VAdd connect(master): %w", err)
	}
	defer conn.Close()

	if err := writeFull(conn, buf.Bytes()); err != nil {
		return fmt.Errorf("VAdd write: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("VAdd read: %w", err)
	}
	if len(line) < 3 || line[:3] != "+OK" {
		return fmt.Errorf("VAdd error: %q", line)
	}
	return nil
}

func (c *ExternalKVClient) VAdd2(key string, vector []float64) error {
	const dim = 1536
	if len(vector) != dim {
		return fmt.Errorf("vector expect dim 1536, got %d", len(vector))
	}

	// 1. float64 → float32，faiss底层用float32
	vec32 := make([]float32, 0, dim)
	for _, v := range vector {
		vec32 = append(vec32, float32(v))
	}
	// 2. 直接序列化小端二进制，不生成任何逗号字符串
	var payloadBuf bytes.Buffer
	for _, f := range vec32 {
		if err := binary.Write(&payloadBuf, binary.LittleEndian, f); err != nil {
			return fmt.Errorf("binary write fail: %w", err)
		}
	}
	payload := payloadBuf.Bytes()

	var buf bytes.Buffer
	buf.WriteString("*3\r\n$4\r\nVADD\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(key)))
	buf.WriteString("\r\n")
	buf.WriteString(key)
	buf.WriteString("\r\n")

	buf.WriteByte('$')
	// buf.WriteString(strconv.Itoa(len(vecStr)))
	buf.WriteString(strconv.Itoa(len(payload)))
	buf.WriteString("\r\n")
	buf.Write(payload)
	buf.WriteString("\r\n")

	conn, err := net.DialTimeout("tcp", c.masterAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("VAdd connect(master): %w", err)
	}
	defer conn.Close()

	if err := writeFull(conn, buf.Bytes()); err != nil {
		return fmt.Errorf("VAdd write: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("VAdd read: %w", err)
	}
	if len(line) < 3 || line[:3] != "+OK" {
		return fmt.Errorf("VAdd error: %q", line)
	}
	return nil
}

// VSearch 向量相似度检索，对接C kvs_vector_search；参数：查询向量、topK
func (c *ExternalKVClient) VSearch(queryVec []float64, topK int) ([]SearchItem, error) {
	vecStr := vecToStr(queryVec)
	var buf bytes.Buffer
	// RESP *3 VSEARCH vec_str topk
	buf.WriteString("*3\r\n$7\r\nVSEARCH\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(vecStr)))
	buf.WriteString("\r\n")
	buf.WriteString(vecStr)
	buf.WriteString("\r\n")
	buf.WriteByte('$')
	kStr := strconv.Itoa(topK)
	buf.WriteString(strconv.Itoa(len(kStr)))
	buf.WriteString("\r\n")
	buf.WriteString(kStr)
	buf.WriteString("\r\n")

	conn, err := net.DialTimeout("tcp", c.masterAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("VSearch connect: %w", err)
	}
	defer conn.Close()

	if err := writeFull(conn, buf.Bytes()); err != nil {
		return nil, fmt.Errorf("VSearch write: %w", err)
	}

	rd := bufio.NewReader(conn)
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if line[0] == '-' {
		return nil, fmt.Errorf("VSearch server error: %s", line)
	}
	if line[0] != '*' {
		return nil, fmt.Errorf("expect RESP array(*), got %q", line)
	}
	arrayLen, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	if arrayLen%2 != 0 {
		return nil, fmt.Errorf("invalid reply array len=%d, must even", arrayLen)
	}
	itemCnt := arrayLen / 2
	res := make([]SearchItem, 0, itemCnt)

	for i := 0; i < itemCnt; i++ {
		key, err := readBulkString(rd)
		if err != nil {
			return nil, err
		}
		distStr, err := readBulkString(rd)
		if err != nil {
			return nil, err
		}
		dist, err := strconv.ParseFloat(distStr, 64)
		if err != nil {
			return nil, fmt.Errorf("parse distance %q err: %w", distStr, err)
		}
		res = append(res, SearchItem{Key: key, Distance: dist})
	}
	return res, nil
}

// readBulkString 读取RESP $len\r\nxxx\r\n
func readBulkString(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if line[0] != '$' {
		return "", fmt.Errorf("want '$' bulk string, got %q", line)
	}
	blenStr := strings.TrimSpace(line[1:])
	blen, err := strconv.Atoi(blenStr)
	if err != nil {
		return "", err
	}
	if blen == -1 {
		return "", nil
	}
	buf := make([]byte, blen+2) // +2 吃掉末尾\r\n
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf[:blen]), nil
}

// vecToStr float64切片转逗号分隔字符串
func vecToStr(vec []float64) string {
	var sb strings.Builder
	for i, f := range vec {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(f, 'f', 6, 64))
	}
	return sb.String()
}

// writeFull 完整写全部字节到conn
func writeFull(conn net.Conn, b []byte) error {
	w := 0
	for w < len(b) {
		n, err := conn.Write(b[w:])
		if err != nil {
			return err
		}
		w += n
	}
	return nil
}

func main() {
	fmt.Println("VADD test client start")
	client := &ExternalKVClient{
		masterAddr: "127.0.0.1:2000",
	}
	// 生成1536维向量
	now := time.Now()

	dim := 1536
	test_count := 0
	for j := 0; j < 10000; j++ {
		testVec := make([]float64, dim)
		for i := 0; i < dim; i++ {
			testVec[i] = float64(i)*0.0001 + float64(j)
		}
		// testKey := "test:faiss:001" + j
		testKey := "test:faiss:" + strconv.Itoa(j)

		err := client.VAdd2(testKey, testVec)
		if err != nil {
			fmt.Printf("VAdd2 failed: %v\n", err)
			return
		} else {
			test_count++
		}
		// fmt.Println("VADD success")
	}
	now2 := time.Now()
	diff := now2.Sub(now)
	time_used := diff.Milliseconds()
	fmt.Printf("time = %d ms \n", time_used)

	// 防止除0
	if time_used <= 0 {
		fmt.Println("time_used is zero, skip qps calc")
		return
	}
	// 转float64做浮点计算
	qps := float64(test_count) / (float64(time_used) / 1000.0)
	avgMs := float64(time_used) / float64(test_count)

	fmt.Printf("total:%d success, qps:%.2f, avg_per_req:%.2f ms\n", test_count, qps, avgMs)

	/*
		在本机测试windows Ubuntu
			原vecotr会转成字符串后到kvstore中再转成float数组中malloc分配到 faiss接口调用
			appuser@appuser:~/chatmsservice/chatgpt-web-backend/tests$ go run kvstore_vadd.go
			VADD test client start
			time = 13820 ms
			total:10000 success, qps:723.59, avg_per_req:1.38 ms

			// 修改后qps 增长3倍, vecotr直接通过字节流写入
			// VADD命令，第二个参数发送原始float32二进制，不再拼接逗号字符串
			VADD test client start
			time = 4234 ms
			total:10000 success, qps:2361.83, avg_per_req:0.42 ms
	*/

	// // 【VSearch】相似度检索，使用同一个向量做查询，topK=5
	// searchResult, err := client.VSearch(testVec, 5)
	// if err != nil {
	// 	fmt.Printf("VSearch failed: %v\n", err)
	// 	return
	// }
	// fmt.Printf("VSearch got %d items\n", len(searchResult))
	// for _, item := range searchResult {
	// 	fmt.Printf("  key=%-20s dist=%.4f\n", item.Key, item.Distance)
	// }
}
