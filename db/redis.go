package db

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Addr        string
	Password    string
	DB          int
	DialTimeout time.Duration
}

type Client struct {
	addr        string
	password    string
	db          int
	dialTimeout time.Duration

	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func NewClient(config Config) (*Client, error) {
	if strings.TrimSpace(config.Addr) == "" {
		return nil, errors.New("redis address is required")
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 5 * time.Second
	}
	addr, password, db, err := normalizeRedisAddress(config.Addr, config.Password, config.DB)
	if err != nil {
		return nil, err
	}
	client := &Client{
		addr:        addr,
		password:    password,
		db:          db,
		dialTimeout: config.DialTimeout,
	}
	if err := client.ping(context.Background()); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.reader = nil
	c.writer = nil
	return err
}

func (c *Client) Get(ctx context.Context, key string) (string, error) {
	reply, err := c.do(ctx, "GET", key)
	if err != nil {
		return "", err
	}
	if reply == nil {
		return "", nil
	}
	value, ok := reply.(string)
	if !ok {
		return "", fmt.Errorf("unexpected redis GET reply %T", reply)
	}
	return value, nil
}

func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	args := []string{"SET", key, value}
	if ttl > 0 {
		seconds := int(ttl / time.Second)
		if seconds <= 0 {
			seconds = 1
		}
		args = append(args, "EX", strconv.Itoa(seconds))
	}
	reply, err := c.do(ctx, args...)
	if err != nil {
		return err
	}
	text, ok := reply.(string)
	if !ok || strings.ToUpper(text) != "OK" {
		return fmt.Errorf("unexpected redis SET reply %v", reply)
	}
	return nil
}

func (c *Client) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	args := []string{"SET", key, value, "NX"}
	if ttl > 0 {
		seconds := int(ttl / time.Second)
		if seconds <= 0 {
			seconds = 1
		}
		args = append(args, "EX", strconv.Itoa(seconds))
	}
	reply, err := c.do(ctx, args...)
	if err != nil {
		return false, err
	}
	if reply == nil {
		return false, nil
	}
	text, ok := reply.(string)
	if !ok || strings.ToUpper(text) != "OK" {
		return false, fmt.Errorf("unexpected redis SETNX reply %v", reply)
	}
	return true, nil
}

func (c *Client) ping(ctx context.Context) error {
	_, err := c.do(ctx, "PING")
	return err
}

func (c *Client) do(ctx context.Context, args ...string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}
	if err := c.writeCommand(ctx, args...); err != nil {
		c.resetConn()
		return nil, err
	}
	reply, err := c.readReply(ctx)
	if err != nil {
		c.resetConn()
		return nil, err
	}
	return reply, nil
}

func (c *Client) ensureConn(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	dialer := &net.Dialer{Timeout: c.dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("dial redis: %w", err)
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	c.writer = bufio.NewWriter(conn)

	if strings.TrimSpace(c.password) != "" {
		if _, err := c.doWithoutLock(ctx, "AUTH", c.password); err != nil {
			c.resetConn()
			return err
		}
	}
	if c.db > 0 {
		if _, err := c.doWithoutLock(ctx, "SELECT", strconv.Itoa(c.db)); err != nil {
			c.resetConn()
			return err
		}
	}
	return nil
}

func (c *Client) doWithoutLock(ctx context.Context, args ...string) (any, error) {
	if err := c.writeCommand(ctx, args...); err != nil {
		return nil, err
	}
	return c.readReply(ctx)
}

func (c *Client) resetConn() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = nil
	c.reader = nil
	c.writer = nil
}

func (c *Client) writeCommand(ctx context.Context, args ...string) error {
	if err := c.setDeadline(ctx); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.writer, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(c.writer, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return c.writer.Flush()
}

func (c *Client) readReply(ctx context.Context) (any, error) {
	if err := c.setDeadline(ctx); err != nil {
		return nil, err
	}
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 {
		return nil, errors.New("invalid redis reply")
	}
	prefix := line[0]
	payload := strings.TrimSuffix(strings.TrimSuffix(line[1:], "\r\n"), "\n")
	switch prefix {
	case '+':
		return payload, nil
	case '-':
		return nil, errors.New(payload)
	case ':':
		return strconv.ParseInt(payload, 10, 64)
	case '$':
		length, err := strconv.Atoi(payload)
		if err != nil {
			return nil, err
		}
		if length == -1 {
			return nil, nil
		}
		buffer := make([]byte, length+2)
		if _, err := c.reader.Read(buffer); err != nil {
			return nil, err
		}
		return string(buffer[:length]), nil
	default:
		return nil, fmt.Errorf("unsupported redis reply prefix %q", prefix)
	}
}

func (c *Client) setDeadline(ctx context.Context) error {
	if c.conn == nil {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		return c.conn.SetDeadline(deadline)
	}
	return c.conn.SetDeadline(time.Now().Add(c.dialTimeout))
}

func normalizeRedisAddress(rawAddr, fallbackPassword string, fallbackDB int) (string, string, int, error) {
	rawAddr = strings.TrimSpace(rawAddr)
	password := strings.TrimSpace(fallbackPassword)
	db := fallbackDB

	if !strings.Contains(rawAddr, "://") {
		return rawAddr, password, db, nil
	}

	parsed, err := url.Parse(rawAddr)
	if err != nil {
		return "", "", 0, fmt.Errorf("parse redis url: %w", err)
	}
	if parsed.Scheme != "redis" {
		return "", "", 0, fmt.Errorf("unsupported redis url scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", "", 0, errors.New("redis url host is required")
	}

	if parsed.User != nil {
		if value, ok := parsed.User.Password(); ok && value != "" {
			password = value
		}
	}

	if parsed.Path != "" && parsed.Path != "/" {
		parsedDB, parseErr := strconv.Atoi(strings.TrimPrefix(parsed.Path, "/"))
		if parseErr != nil {
			return "", "", 0, fmt.Errorf("parse redis db from url: %w", parseErr)
		}
		db = parsedDB
	}

	if queryDB := parsed.Query().Get("db"); queryDB != "" {
		parsedDB, parseErr := strconv.Atoi(queryDB)
		if parseErr != nil {
			return "", "", 0, fmt.Errorf("parse redis db query parameter: %w", parseErr)
		}
		db = parsedDB
	}

	return parsed.Host, password, db, nil
}
