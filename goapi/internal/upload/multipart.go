package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
)

// ErrNotMultipart 表示请求不是 multipart/form-data（没有 boundary / 根本没有体）。
// 调用方按「没有收到文件」处理（PHP 里 $_FILES 为空）。
var ErrNotMultipart = errors.New("不是 multipart/form-data 请求")

// maxFieldBytes 是单个**文本部件**的读取上限。PHP 那边由 post_max_size 兜住，
// 这里只做防御：正常参数（video_w/video_h/video_duration/video_size/type/token）
// 都是几十字节，超过就截断（截断后参数校验照样能给出正确的中文报错）。
const maxFieldBytes = 1 << 20

// FilePart 是流式落到临时文件的 file 部件。
//
// Path 指向 0600 的临时文件，**调用方必须 defer os.Remove(Path)**（ReadMultipart
// 在出错时会自己清理，成功返回后所有权归调用方）。
type FilePart struct {
	Field     string
	Filename  string
	Size      int64  // 实际写入临时文件的字节数（截断时为 maxBytes+1）
	Path      string // 临时文件路径
	SHA256    string // 内容 sha256(hex)，可直接当 S3 SigV4 的 payload hash
	Truncated bool   // 是否因为超过 maxBytes 提前停止
}

// MultipartResult 是一次流式解析的结果。
type MultipartResult struct {
	File          *FilePart  // 没有 file 部件时为 nil
	Fields        url.Values // 所有文本部件（含 file 之前/之后的）
	ContentLength int64
}

// Field 取文本部件（等价 PHP 的 $_POST[key]）。
func (m *MultipartResult) Field(key string) (string, bool) {
	if m == nil || m.Fields == nil {
		return "", false
	}
	vs, ok := m.Fields[key]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

// ReadMultipart 流式解析 multipart/form-data：**绝不调用 r.ParseMultipartForm**。
//
// 为什么必须流式（契约第 1 节）：Android 客户端（ApiClient.kt 的
// socialVideoUpload）先发 file 部件，**之后**才发 video_w/video_h/
// video_duration/video_size 四个文本部件。所以只能「边读边把 file 落临时文件 →
// 读完剩余部件返回」，峰值内存 O(1)，且大文件不会进内存（ParseMultipartForm
// 会把超出内存阈值的部分静默落到它自己管理的磁盘上，不可控）。
//
// maxBytes > 0 时，file 部件累计写到 maxBytes+1 字节即停止（返回 Truncated=true），
// 由调用方按各自的文案报错（PHP 里就是 `$file['size'] > $max` 的分支）。
//
// 顺序与归属都保留在返回值的 Fields 里，因此「file 在文本部件之前」的请求
// 与「文本部件在 file 之前」的请求都能正确取到参数。
func ReadMultipart(r *http.Request, fileField, tmpDir string, maxBytes int64) (*MultipartResult, error) {
	res := &MultipartResult{Fields: url.Values{}, ContentLength: r.ContentLength}

	mr, err := r.MultipartReader()
	if err != nil {
		return res, ErrNotMultipart
	}

	var (
		part *FilePart
		file *os.File
	)
	// closeFile 只在**出错**路径调用：成功返回时临时文件的所有权交给调用方。
	closeFile := func(remove bool) {
		if file == nil {
			return
		}
		name := file.Name()
		_ = file.Close()
		if remove {
			_ = os.Remove(name)
		}
		file = nil
	}

	for {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			closeFile(true)
			return res, perr
		}

		if p.FormName() == fileField && p.FileName() != "" {
			// PHP 对重复的同名 file 部件取最后一个；这里同样覆盖。
			closeFile(true)
			f, cerr := os.CreateTemp(tmpDir, "goapi-upload-*")
			if cerr != nil {
				_ = p.Close()
				return res, cerr
			}
			file = f
			h := sha256.New()
			size, truncated, werr := drainToFile(f, h, p, maxBytes)
			_ = p.Close()
			if werr != nil {
				closeFile(true)
				return res, werr
			}
			part = &FilePart{
				Field:     fileField,
				Filename:  p.FileName(),
				Size:      size,
				Path:      f.Name(),
				SHA256:    hex.EncodeToString(h.Sum(nil)),
				Truncated: truncated,
			}
			res.File = part
			if truncated {
				// 已经超过上限，后面的参数读了也没用（调用方必然报错）。
				closeFile(false)
				return res, nil
			}
			continue
		}

		body, rerr := io.ReadAll(io.LimitReader(p, maxFieldBytes))
		_ = p.Close()
		if rerr != nil {
			closeFile(true)
			return res, rerr
		}
		if name := p.FormName(); name != "" {
			res.Fields.Add(name, string(body))
		}
	}
	closeFile(false)
	return res, nil
}

// drainToFile 把部件内容写进临时文件，同时算 sha256；超过 maxBytes 时只多写 1 字节
// 就停下（让调用方能用 `size > maxBytes` 复刻 PHP 的大小判断），返回 (size, truncated, err)。
func drainToFile(dst io.Writer, h hash.Hash, src io.Reader, maxBytes int64) (int64, bool, error) {
	w := io.MultiWriter(dst, h)
	if maxBytes <= 0 {
		n, err := io.Copy(w, src)
		return n, false, err
	}
	n, err := io.Copy(w, io.LimitReader(src, maxBytes+1))
	if err != nil {
		return n, false, err
	}
	return n, n > maxBytes, nil
}

// Open 打开临时文件（调用方负责 Close）。流式 PUT 到 S3 时用它，
// 不把大文件读进内存。
func (f *FilePart) Open() (*os.File, error) { return os.Open(f.Path) }

// Head 读取临时文件头部 n 字节（视频容器判定只需要前 4096 字节）。
func (f *FilePart) Head(n int64) ([]byte, error) {
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return io.ReadAll(io.LimitReader(fh, n))
}
