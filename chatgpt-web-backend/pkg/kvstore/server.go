package kvstore

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"k8s.io/klog/v2"
)

// KVStore 基于 RESP 协议的内存键值存储服务
type KVStore struct {
	data sync.Map
}

// NewKVStore 创建 KV 存储实例
func NewKVStore() *KVStore {
	return &KVStore{}
}

// Set 存储键值对
func (s *KVStore) Set(key, value string) {
	s.data.Store(key, value)
}

// Get 获取键值对
func (s *KVStore) Get(key string) (string, bool) {
	v, ok := s.data.Load(key)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// Incr 自增计数器，返回新值（初始为 0，调用后从 1 开始）
func (s *KVStore) Incr(key string) int {
	for {
		v, _ := s.data.LoadOrStore(key, "0")
		old, _ := strconv.Atoi(v.(string))
		newVal := old + 1
		ok := s.data.CompareAndSwap(key, v, strconv.Itoa(newVal))
		if ok {
			return newVal
		}
	}
}

// StartRESPServer 在指定地址启动 RESP 协议 TCP 服务
func StartRESPServer(addr string) error {
	store := NewKVStore()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("kvstore listen error: %w", err)
	}
	klog.Infof("KV Store RESP Server listening on %s", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			klog.Errorf("kvstore accept error: %v", err)
			continue
		}
		go store.handleConn(conn)
	}
}

func (s *KVStore) handleConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for {
		args, err := parseRESP(reader)
		if err != nil {
			if err != io.EOF {
				klog.Errorf("kvstore parse error: %v", err)
			}
			return
		}

		if len(args) == 0 {
			continue
		}

		switch args[0] {
		case "SET":
			if len(args) != 3 {
				conn.Write([]byte("-ERR wrong number of arguments for SET\r\n"))
				continue
			}
			s.Set(args[1], args[2])
			klog.Infof("KV SET: %s", args[1])
			conn.Write([]byte("+OK\r\n"))

		case "GET":
			if len(args) != 2 {
				conn.Write([]byte("-ERR wrong number of arguments for GET\r\n"))
				continue
			}
			val, ok := s.Get(args[1])
			if !ok {
				conn.Write([]byte("$-1\r\n"))
			} else {
				writeBulkStr(conn, val)
			}

		case "PING":
			conn.Write([]byte("+PONG\r\n"))

		case "INCR":
			if len(args) != 2 {
				conn.Write([]byte("-ERR wrong number of arguments for INCR\r\n"))
				continue
			}
			n := s.Incr(args[1])
			fmt.Fprintf(conn, ":%d\r\n", n)

		default:
			conn.Write([]byte("-ERR unknown command\r\n"))
		}
	}
}

// parseRESP 解析 RESP 协议，返回命令参数列表
func parseRESP(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	if len(line) < 2 || line[0] != '*' {
		return nil, fmt.Errorf("invalid RESP: expected array, got: %s", line)
	}

	count, err := strconv.Atoi(line[1 : len(line)-2])
	if err != nil {
		return nil, fmt.Errorf("invalid array count: %s", line)
	}

	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		arg, err := readBulkString(reader)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}

	return args, nil
}

func readBulkString(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	if len(line) < 2 || line[0] != '$' {
		return "", fmt.Errorf("invalid bulk string header: %s", line)
	}

	length, err := strconv.Atoi(line[1 : len(line)-2])
	if err != nil {
		return "", fmt.Errorf("invalid bulk string length: %s", line)
	}

	if length < 0 {
		return "", nil // null bulk string
	}

	buf := make([]byte, length+2) // +2 for \r\n
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", err
	}

	return string(buf[:length]), nil
}
