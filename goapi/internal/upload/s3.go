package upload

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// UnsignedPayload 是流式 PUT 用的字面量（对齐 api.php:2768 upload_apk 的
// 流式签名语义与 x-amz-content-sha256 头）。
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// safeKeyRe 限制对象键的字符集。生成的 key 只可能是
// `<dir>/<YmdHis>_<8hex>.<ext>`（dir ∈ avatars/chat/images/files/apk），
// 全是 URL 安全字符，因此「签名用的 canonical path」与「实际请求路径」逐字相同，
// 不会出现 PHP 那种「路径未转义」与 Go 转义策略不一致的坑。
var safeKeyRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// Options 是 S3 客户端的构造参数（全部来自 /etc/fengling/goapi.env，绝不落仓库）。
type Options struct {
	Endpoint  string // https://cn-nb1.rains3.com
	Bucket    string // fenglin
	Region    string // cn-nb1
	AccessKey string
	SecretKey string
	PublicURL string // 公共读前缀，最终 url = PublicURL + "/" + key

	PutTimeout    time.Duration // 整段内存上传（PHP 300s）
	StreamTimeout time.Duration // 流式上传（APK，PHP 600s）
	Logger        *slog.Logger
	// Now 可注入（单测里固定时间，便于断言签名）。
	Now func() time.Time
}

// Client 是最小 S3 客户端：只需要 PUT（字节/流）与 DELETE。
type Client struct {
	endpoint  string
	bucket    string
	region    string
	accessKey string
	secretKey string
	publicURL string

	http          *http.Client
	putTimeout    time.Duration
	streamTimeout time.Duration
	log           *slog.Logger
	now           func() time.Time
}

// StatusError 携带服务端返回的 HTTP 状态码（供 upload_apk 复刻
// 「上传对象存储失败 (HTTP N)」）。
type StatusError struct {
	Status int
	Op     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("s3 %s 失败: HTTP %d", e.Op, e.Status)
}

// StatusOf 取错误里的 HTTP 状态码；网络层错误返回 0（PHP 那边 curl 拿不到状态码时也是 0）。
func StatusOf(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status
	}
	return 0
}

// NewClient 校验必需配置并建客户端。**缺任一项都返回错误**（main 里据此启动失败）。
func NewClient(opt Options) (*Client, error) {
	missing := make([]string, 0, 6)
	for name, v := range map[string]string{
		"S3_ENDPOINT":   opt.Endpoint,
		"S3_BUCKET":     opt.Bucket,
		"S3_REGION":     opt.Region,
		"S3_ACCESS_KEY": opt.AccessKey,
		"S3_SECRET_KEY": opt.SecretKey,
		"S3_PUBLIC_URL": opt.PublicURL,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("S3 配置缺失: %s", strings.Join(missing, ", "))
	}
	if _, err := url.Parse(opt.Endpoint); err != nil {
		return nil, fmt.Errorf("S3_ENDPOINT 非法: %w", err)
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	if opt.PutTimeout <= 0 {
		opt.PutTimeout = 300 * time.Second
	}
	if opt.StreamTimeout <= 0 {
		opt.StreamTimeout = 600 * time.Second
	}
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		endpoint:      strings.TrimRight(opt.Endpoint, "/"),
		bucket:        opt.Bucket,
		region:        opt.Region,
		accessKey:     opt.AccessKey,
		secretKey:     opt.SecretKey,
		publicURL:     strings.TrimRight(opt.PublicURL, "/"),
		http:          &http.Client{Transport: tr},
		putTimeout:    opt.PutTimeout,
		streamTimeout: opt.StreamTimeout,
		log:           log,
		now:           now,
	}, nil
}

// PublicURL 返回公共读前缀（视频清理反推 key 时要用）。
func (c *Client) PublicURL() string { return c.publicURL }

// ObjectURL 复刻 PHP 的 `S3_PUBLIC_URL . '/' . $key`。
func (c *Client) ObjectURL(key string) string { return c.publicURL + "/" + key }

// KeyFromURL 对齐 api.php:4005 video_key_from_url：只接受本站公共读地址，
// 其它一律返回空串（清理时就不会误删别的对象）。
func (c *Client) KeyFromURL(u string) string {
	prefix := c.publicURL + "/"
	if u == "" || !strings.HasPrefix(u, prefix) {
		return ""
	}
	return strings.TrimPrefix(u, prefix)
}

// PutBytes 走「整段字节 + 真实 sha256 签名」路径（对齐 api.php:109 s3_upload_bytes）。
func (c *Client) PutBytes(ctx context.Context, key, contentType string, body []byte) error {
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(ctx, c.putTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPut, key, contentType, payloadHash, bytes.NewReader(body), int64(len(body)))
}

// PutStream 走流式 PUT：payloadHash 传 UnsignedPayload（APK，对齐 api.php:2756）
// 或内容真实的 sha256 hex（视频，对齐 s3_upload_bytes 的签名语义）。
func (c *Client) PutStream(ctx context.Context, key, contentType string, body io.Reader, size int64, payloadHash string) error {
	ctx, cancel := context.WithTimeout(ctx, c.streamTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPut, key, contentType, payloadHash, body, size)
}

// Delete 对齐 api.php:129 s3_delete（空体 sha256 签名）。
func (c *Client) Delete(ctx context.Context, key string) error {
	empty := sha256.Sum256(nil)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return c.do(ctx, http.MethodDelete, key, "", hex.EncodeToString(empty[:]), nil, 0)
}

func (c *Client) do(ctx context.Context, method, key, contentType, payloadHash string, body io.Reader, size int64) error {
	if !safeKeyRe.MatchString(key) {
		return fmt.Errorf("对象键含非法字符, 拒绝请求: %q", key)
	}
	path := "/" + c.bucket + "/" + key
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.ContentLength = size
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.sign(req, payloadHash)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 读掉响应体（复用连接），但只留少量日志用信息，绝不回显签名/密钥。
	tail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Warn("S3 请求失败",
			"op", strings.ToLower(method), "key", key,
			"status", resp.StatusCode, "body", strings.TrimSpace(string(tail)))
		return &StatusError{Status: resp.StatusCode, Op: strings.ToLower(method)}
	}
	return nil
}

// sign 复刻 api.php:57-77 s3_sign_v4：只签 host;x-amz-content-sha256;x-amz-date，
// canonical path 是 /<bucket>/<key>（path-style），Content-Type 不参与签名。
func (c *Client) sign(req *http.Request, payloadHash string) {
	now := c.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	host := req.URL.Hostname() // parse_url(..., PHP_URL_HOST)：不带端口，与 PHP 一致
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := req.Method + "\n" + req.URL.Path + "\n\n" +
		canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	reqSum := sha256.Sum256([]byte(canonicalRequest))

	scope := dateStamp + "/" + c.region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(reqSum[:])

	kDate := hmacSHA256([]byte("AWS4"+c.secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, c.region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.accessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}
