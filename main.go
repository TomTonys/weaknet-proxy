package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// 默认配置：监听 8107 端口，网速限制为 5 KB/s (2G 弱网)
const (
	LISTEN_PORT       = ":8107"
	TARGET_SPEED_KBPS = 5 // 单位: KB/s
)

// 限速包装器
type RateLimitedReader struct {
	reader      io.Reader
	bytesPerSec int64
}

func NewRateLimitedReader(r io.Reader, kbps int64) *RateLimitedReader {
	return &RateLimitedReader{
		reader:      r,
		bytesPerSec: kbps * 1024,
	}
}

func (r *RateLimitedReader) Read(p []byte) (n int, err error) {
	chunkSize := 2048
	if len(p) > chunkSize {
		p = p[:chunkSize]
	}

	start := time.Now()
	n, err = r.reader.Read(p)
	if n > 0 {
		expectedDuration := time.Duration(float64(n) / float64(r.bytesPerSec) * float64(time.Second))
		actualDuration := time.Since(start)
		if expectedDuration > actualDuration {
			time.Sleep(expectedDuration - actualDuration)
		}
	}
	return n, err
}

// SOCKS5 握手处理
func handleSocks5(clientConn net.Conn) {
	defer clientConn.Close()

	// 1. 协商阶段
	buf := make([]byte, 256)
	if _, err := io.ReadFull(clientConn, buf[:2]); err != nil {
		return
	}
	nmethods := int(buf[1])
	if _, err := io.ReadFull(clientConn, buf[:nmethods]); err != nil {
		return
	}
	// 回复无需认证 (0x00)
	clientConn.Write([]byte{0x05, 0x00})

	// 2. 请求阶段
	if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
		return
	}

	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // 域名
		if _, err := io.ReadFull(clientConn, buf[:1]); err != nil {
			return
		}
		domainLen := int(buf[0])
		if _, err := io.ReadFull(clientConn, buf[:domainLen]); err != nil {
			return
		}
		host = string(buf[:domainLen])
	default:
		return
	}

	if _, err := io.ReadFull(clientConn, buf[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[:2])
	targetAddr := net.JoinHostPort(host, strconv.Itoa(int(port)))

	// 3. 连接目标服务器
	targetConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		// 回复连接失败
		clientConn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer targetConn.Close()

	// 回复连接成功
	clientConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// 4. 双向透传流量并限速
	limitedClient := NewRateLimitedReader(clientConn, TARGET_SPEED_KBPS)
	limitedTarget := NewRateLimitedReader(targetConn, TARGET_SPEED_KBPS)

	go io.Copy(targetConn, limitedClient)
	io.Copy(clientConn, limitedTarget)
}

func main() {
	listener, err := net.Listen("tcp", LISTEN_PORT)
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	fmt.Printf("SOCKS5 弱网代理已启动，监听端口 %s，网速限制为: %d KB/s\n", LISTEN_PORT, TARGET_SPEED_KBPS)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go handleSocks5(conn)
	}
}