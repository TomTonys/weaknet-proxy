package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenPort      = "8107"
	defaultTargetSpeedKBPS = 30 // 单位: KB/s
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
func handleSocks5(clientConn net.Conn, targetSpeedKBPS int64) {
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
	limitedClient := NewRateLimitedReader(clientConn, targetSpeedKBPS)
	limitedTarget := NewRateLimitedReader(targetConn, targetSpeedKBPS)

	go io.Copy(targetConn, limitedClient)
	io.Copy(clientConn, limitedTarget)
}

func main() {
	speed := flag.Int64("speed", defaultTargetSpeedKBPS, "限速，单位: KB/s")
	listen := flag.String("listen", defaultListenPort, "监听端口，例如 8107")
	flag.Parse()

	if *speed <= 0 {
		panic("speed must be greater than 0")
	}

	listenAddr := *listen
	if !strings.Contains(listenAddr, ":") {
		listenAddr = ":" + listenAddr
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	fmt.Printf("SOCKS5 弱网代理已启动，监听端口 %s，网速限制为: %d KB/s\n", listenAddr, *speed)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go handleSocks5(conn, *speed)
	}
}
