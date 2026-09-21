package kvstore

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// ExternalKVClient 外部 KV Store 客户端（RESP 协议，极简）
// masterAddr: 主节点地址（用于写入：SET、VADD）
// slaveAddr:  从节点地址（用于查询：GET、VSEARCH）
type ExternalKVClient struct {
	masterAddr string
	slaveAddr  string
}

func NewExternalKVClient(masterAddr, slaveAddr string) *ExternalKVClient {
	return &ExternalKVClient{
		masterAddr: masterAddr,
		slaveAddr:  slaveAddr,
	}
}

// ---------- 底层 Set / Get（bytes.Buffer 一次写出，零 Sprintf）----------

// writeFull 确保所有数据完整写入
func writeFull(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (c *ExternalKVClient) Set(key, value string) error {
	var buf bytes.Buffer
	buf.WriteString("*3\r\n$3\r\nSET\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(key)))
	buf.WriteString("\r\n")
	buf.WriteString(key)
	buf.WriteString("\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(value)))
	buf.WriteString("\r\n")
	buf.WriteString(value)
	buf.WriteString("\r\n")

	conn, err := net.DialTimeout("tcp", c.masterAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("KV connect(master): %w", err)
	}
	defer conn.Close()

	if err := writeFull(conn, buf.Bytes()); err != nil {
		return fmt.Errorf("KV write: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("KV read OK: %w", err)
	}
	if len(line) < 3 || line[:3] != "+OK" {
		return fmt.Errorf("KV SET error: %q", line)
	}
	return nil
}

func (c *ExternalKVClient) Get(key string) (string, bool, error) {
	var buf bytes.Buffer
	buf.WriteString("*2\r\n$3\r\nGET\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(key)))
	buf.WriteString("\r\n")
	buf.WriteString(key)
	buf.WriteString("\r\n")

	conn, err := net.DialTimeout("tcp", c.slaveAddr, 5*time.Second)
	if err != nil {
		return "", false, fmt.Errorf("KV connect(slave): %w", err)
	}
	defer conn.Close()

	if err := writeFull(conn, buf.Bytes()); err != nil {
		return "", false, fmt.Errorf("KV write: %w", err)
	}

	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", false, fmt.Errorf("KV read: %w", err)
	}
	if line == "$-1\r\n" || line == "$0\r\n" || line == "$-1\n" || line == "$0\n" {
		return "", false, nil
	}
	if len(line) < 2 || line[0] != '$' {
		return "", false, fmt.Errorf("KV GET bad resp: %q", line)
	}
	n, err := strconv.Atoi(line[1 : len(line)-2])
	if err != nil || n <= 0 {
		return "", false, nil
	}
	b := make([]byte, n+2)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", false, fmt.Errorf("KV GET read %d bytes: %w", n, err)
	}
	return string(b[:n]), true, nil
}

/**
 * ---------- 消息存储 ----------
 * NextIndex 遍历 msg1,msg2... 找到第一个不存在的 index
 *废弃
 */
func (c *ExternalKVClient) NextIndex() (int, error) {
	i := 1
	for {
		_, ok, err := c.Get("msg" + strconv.Itoa(i))
		if err != nil {
			return 0, err
		}
		if !ok {
			return i, nil
		}
		i++
	}
}

func (c *ExternalKVClient) PutMsg(key string, text string) error {
	return c.Set(key, text)
}

// GetMsg 读一条消息，自动 base64 解码
// func (c *ExternalKVClient) GetMsg(index int) (string, bool, error) {
// 	key := "msg" + strconv.Itoa(index)
// 	val, ok, err := c.Get(key)
// 	if err != nil || !ok {
// 		return "", ok, err
// 	}
// 	raw, err := base64.StdEncoding.DecodeString(val)
// 	if err != nil {
// 		log.Printf("KV msg%d base64 decode error: %v", index, err)
// 		return "", false, nil
// 	}
// 	return string(raw), true, nil
// }

// ---------- 向量操作 VADD / VSEARCH ----------

// vecToStr 将 []float64 转为逗号分隔字符串，如 "1.0,0.0,0.0,0.0"
func vecToStr(v []float64) string {
	if len(v) == 0 {
		return ""
	}
	var buf bytes.Buffer
	for i, f := range v {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
	}
	return buf.String()
}

// VAdd 存储向量到 KV Store: key=问题原文, value=逗号分隔浮点数向量（写入主节点）
// func (c *ExternalKVClient) VAdd(key string, vector []float64) error {
// 	vecStr := vecToStr(vector)

// 	var buf bytes.Buffer
// 	buf.WriteString("*3\r\n$4\r\nVADD\r\n")
// 	buf.WriteByte('$')
// 	buf.WriteString(strconv.Itoa(len(key)))
// 	buf.WriteString("\r\n")
// 	buf.WriteString(key)
// 	buf.WriteString("\r\n")
// 	buf.WriteByte('$')
// 	buf.WriteString(strconv.Itoa(len(vecStr)))
// 	buf.WriteString("\r\n")
// 	buf.WriteString(vecStr)
// 	buf.WriteString("\r\n")

// 	conn, err := net.DialTimeout("tcp", c.masterAddr, 5*time.Second)
// 	if err != nil {
// 		return fmt.Errorf("VAdd connect(master): %w", err)
// 	}
// 	defer conn.Close()

// 	if err := writeFull(conn, buf.Bytes()); err != nil {
// 		return fmt.Errorf("VAdd write: %w", err)
// 	}
// 	line, err := bufio.NewReader(conn).ReadString('\n')
// 	if err != nil {
// 		return fmt.Errorf("VAdd read: %w", err)
// 	}
// 	if len(line) < 3 || line[:3] != "+OK" {
// 		return fmt.Errorf("VAdd error: %q", line)
// 	}
// 	return nil
// }

func (c *ExternalKVClient) VAdd(key string, vector []float64) error {
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

// VSearch 向量相似度搜索，返回 top-k 匹配的问题原文 key（查询从节点）
// func (c *ExternalKVClient) VSearch(vector []float64, k int) ([]string, error) {
// 	vecStr := vecToStr(vector)
// 	kStr := strconv.Itoa(k)

// 	var buf bytes.Buffer
// 	buf.WriteString("*3\r\n$7\r\nVSEARCH\r\n")
// 	buf.WriteByte('$')
// 	buf.WriteString(strconv.Itoa(len(vecStr)))
// 	buf.WriteString("\r\n")
// 	buf.WriteString(vecStr)
// 	buf.WriteString("\r\n")
// 	buf.WriteByte('$')
// 	buf.WriteString(strconv.Itoa(len(kStr)))
// 	buf.WriteString("\r\n")
// 	buf.WriteString(kStr)
// 	buf.WriteString("\r\n")

// 	conn, err := net.DialTimeout("tcp", c.slaveAddr, 5*time.Second)
// 	if err != nil {
// 		return nil, fmt.Errorf("VSearch connect(slave): %w", err)
// 	}
// 	defer conn.Close()

// 	if err := writeFull(conn, buf.Bytes()); err != nil {
// 		return nil, fmt.Errorf("VSearch write: %w", err)
// 	}

// 	log.Printf("ip:prt %v", c.slaveAddr)
// 	klog.Infof("11111111: %v 无结果", c.slaveAddr)

// 	r := bufio.NewReader(conn)
// 	keys, err := readRESPStringArray(r)
// 	if err != nil {
// 		return nil, fmt.Errorf("VSearch read response: %w", err)
// 	}
// 	return keys, nil
// }

// VSearch 向量相似度搜索，返回 top-k 匹配的问题原文 key（查询从节点）
func (c *ExternalKVClient) VSearch(vector []float64, k int) ([]string, error) {
	const dim = 1536
	if len(vector) != dim {
		return nil, fmt.Errorf("vector expect dim 1536, got %d", len(vector))
	}

	// 1. float64 → float32，与 VAdd 保持一致
	vec32 := make([]float32, 0, dim)
	for _, v := range vector {
		vec32 = append(vec32, float32(v))
	}

	// 2. 小端二进制序列化，与 VAdd 保持一致
	var payloadBuf bytes.Buffer
	for _, f := range vec32 {
		if err := binary.Write(&payloadBuf, binary.LittleEndian, f); err != nil {
			return nil, fmt.Errorf("binary write fail: %w", err)
		}
	}
	payload := payloadBuf.Bytes()

	kStr := strconv.Itoa(k)

	var buf bytes.Buffer
	buf.WriteString("*3\r\n$7\r\nVSEARCH\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(payload)))
	buf.WriteString("\r\n")
	buf.Write(payload)
	buf.WriteString("\r\n")
	buf.WriteByte('$')
	buf.WriteString(strconv.Itoa(len(kStr)))
	buf.WriteString("\r\n")
	buf.WriteString(kStr)
	buf.WriteString("\r\n")

	conn, err := net.DialTimeout("tcp", c.slaveAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("VSearch connect(slave): %w", err)
	}
	defer conn.Close()

	// 设置读超时
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	if err := writeFull(conn, buf.Bytes()); err != nil {
		return nil, fmt.Errorf("VSearch write: %w", err)
	}

	r := bufio.NewReader(conn)

	fmt.Printf("before readRESPStringArray\n")
	keys, err := readRESPStringArray(r)
	fmt.Printf("after readRESPStringArray, len(keys)=%v, err=%v\n", len(keys), err)

	if err != nil {
		return nil, fmt.Errorf("VSearch read response: %w", err)
	}

	// 解析：偶数下标0,2,4...是key；奇数是distance，跳过
	var resultKeys []string
	for i := 0; i < len(keys); i += 2 {
		key := keys[i]
		// 过滤unknown
		if key != "unknown" {
			resultKeys = append(resultKeys, key)
		}
	}

	return keys, nil
}

// readRESPStringArray 解析 RESP 数组响应，返回字符串列表
func readRESPStringArray(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[0] != '*' {
		// 可能是错误响应
		return nil, fmt.Errorf("VSearch unexpected response: %q", line)
	}
	count, err := strconv.Atoi(line[1 : len(line)-2])
	if err != nil {
		return nil, fmt.Errorf("VSearch bad array count: %s", line)
	}
	if count <= 0 {
		return nil, nil
	}
	result := make([]string, 0, count)
	for i := 0; i < count; i++ {
		s, err := readBulkStr(r)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, nil
}

// readBulkStr 从 reader 读取一个 RESP Bulk String
func readBulkStr(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 2 || line[0] != '$' {
		return "", fmt.Errorf("expected bulk string, got: %q", line)
	}
	length, err := strconv.Atoi(line[1 : len(line)-2])
	if err != nil {
		return "", fmt.Errorf("bad bulk string length: %s", line)
	}
	if length < 0 {
		return "", nil
	}
	buf := make([]byte, length+2)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf[:length]), nil
}

// writeBulkStr 供 server.go 使用：写入 RESP Bulk String
func writeBulkStr(w io.Writer, s string) error {
	if _, err := io.WriteString(w, "$"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, strconv.Itoa(len(s))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\r\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, s); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}
