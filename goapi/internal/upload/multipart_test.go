package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// buildBody 按 parts 的顺序拼 multipart 请求体；kind="file" 用 filename 作为文件部件。
type part struct {
	name     string
	value    string
	isFile   bool
	filename string
}

func buildBody(t *testing.T, parts []part) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.isFile {
			fw, err := w.CreateFormFile(p.name, p.filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write([]byte(p.value)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := w.WriteField(p.name, p.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

// 契约第 1 节的核心用例：file 部件在**前**、video_* 文本部件在**后**
// （Android ApiClient.kt 就是这么发的）。流式读取必须仍能拿到全部参数。
func TestReadMultipartFileFirstThenParams(t *testing.T) {
	content := "FAKE-VIDEO-BYTES-0123456789"
	body, ctype := buildBody(t, []part{
		{name: "file", value: content, isFile: true, filename: "clip.mp4"},
		{name: "video_w", value: "1080"},
		{name: "video_h", value: "1920"},
		{name: "video_duration", value: "15"},
		{name: "video_size", value: "12345678"},
	})
	req := httptest.NewRequest("POST", "/api.php?action=social_video_upload", body)
	req.Header.Set("Content-Type", ctype)

	res, err := ReadMultipart(req, "file", "", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.File.Path)
	if res.File == nil {
		t.Fatal("file 部件丢失")
	}
	if res.File.Filename != "clip.mp4" {
		t.Fatalf("filename=%q", res.File.Filename)
	}
	if res.File.Size != int64(len(content)) || res.File.Truncated {
		t.Fatalf("size=%d truncated=%v", res.File.Size, res.File.Truncated)
	}
	sum := sha256.Sum256([]byte(content))
	if res.File.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256=%s", res.File.SHA256)
	}
	// 临时文件内容与权限
	got, err := os.ReadFile(res.File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("临时文件内容=%q", got)
	}
	if fi, err := os.Stat(res.File.Path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("临时文件权限=%v, 期望 0600", fi.Mode().Perm())
	}
	// 参数必须齐全（这就是「不能先读参数再决定收不收文件」的原因）
	want := map[string]string{"video_w": "1080", "video_h": "1920", "video_duration": "15", "video_size": "12345678"}
	for k, v := range want {
		if got, ok := res.Field(k); !ok || got != v {
			t.Fatalf("%s=%q ok=%v, 期望 %q", k, got, ok, v)
		}
	}
}

// 文本部件在 file 之前也要能读（PHP 的 $_POST 与顺序无关）。
func TestReadMultipartParamsFirstThenFile(t *testing.T) {
	body, ctype := buildBody(t, []part{
		{name: "video_w", value: "720"},
		{name: "file", value: "ABCDEF", isFile: true, filename: "a.mov"},
	})
	req := httptest.NewRequest("POST", "/api.php?action=social_video_upload", body)
	req.Header.Set("Content-Type", ctype)

	res, err := ReadMultipart(req, "file", "", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.File.Path)
	if res.File == nil || res.File.Size != 6 {
		t.Fatalf("file=%+v", res.File)
	}
	if got, ok := res.Field("video_w"); !ok || got != "720" {
		t.Fatalf("video_w=%q ok=%v", got, ok)
	}
}

// 超过 maxBytes：只多写 1 字节就停（让调用方用 size>max 复刻 PHP 的判断）。
func TestReadMultipartTruncatesAtLimit(t *testing.T) {
	body, ctype := buildBody(t, []part{
		{name: "file", value: strings.Repeat("x", 1000), isFile: true, filename: "big.png"},
	})
	req := httptest.NewRequest("POST", "/api.php?action=user_avatar", body)
	req.Header.Set("Content-Type", ctype)

	res, err := ReadMultipart(req, "file", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.File.Path)
	if res.File == nil {
		t.Fatal("截断时也应返回 FilePart(带 Size 供调用方报错)")
	}
	if !res.File.Truncated {
		t.Fatal("应标记 Truncated")
	}
	if res.File.Size != 11 {
		t.Fatalf("size=%d, 期望 maxBytes+1=11", res.File.Size)
	}
}

func TestReadMultipartNotMultipart(t *testing.T) {
	req := httptest.NewRequest("POST", "/api.php?action=user_avatar", bytes.NewReader([]byte("plain text")))
	req.Header.Set("Content-Type", "text/plain")
	res, err := ReadMultipart(req, "file", "", 1<<20)
	if !errors.Is(err, ErrNotMultipart) {
		t.Fatalf("err=%v", err)
	}
	if res == nil || res.File != nil {
		t.Fatalf("res=%+v", res)
	}
}

// PHP 对重复的同名 file 部件取最后一个。
func TestReadMultipartDuplicateFileTakesLast(t *testing.T) {
	body, ctype := buildBody(t, []part{
		{name: "file", value: "FIRST", isFile: true, filename: "1.png"},
		{name: "file", value: "SECOND", isFile: true, filename: "2.png"},
	})
	req := httptest.NewRequest("POST", "/api.php?action=user_avatar", body)
	req.Header.Set("Content-Type", ctype)

	res, err := ReadMultipart(req, "file", "", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.File.Path)
	if res.File.Filename != "2.png" {
		t.Fatalf("filename=%q", res.File.Filename)
	}
	if got, _ := os.ReadFile(res.File.Path); string(got) != "SECOND" {
		t.Fatalf("content=%q", got)
	}
}

// 只有 filename 非空的部件才算 $_FILES（PHP 语义）；无 filename 的同名部件进 $_POST。
func TestReadMultipartFieldWithoutFilenameIsNotFile(t *testing.T) {
	body, ctype := buildBody(t, []part{{name: "file", value: "abc"}})
	req := httptest.NewRequest("POST", "/api.php?action=user_avatar", body)
	req.Header.Set("Content-Type", ctype)

	res, err := ReadMultipart(req, "file", "", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if res.File != nil {
		t.Fatalf("不该产生 FilePart: %+v", res.File)
	}
	if got, ok := res.Field("file"); !ok || got != "abc" {
		t.Fatalf("file=%q ok=%v", got, ok)
	}
}

func TestFilePartOpenAndHead(t *testing.T) {
	body, ctype := buildBody(t, []part{
		{name: "file", value: "0123456789", isFile: true, filename: "x.mp4"},
	})
	req := httptest.NewRequest("POST", "/api.php?action=social_video_upload", body)
	req.Header.Set("Content-Type", ctype)

	res, err := ReadMultipart(req, "file", "", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.File.Path)
	head, err := res.File.Head(4)
	if err != nil || string(head) != "0123" {
		t.Fatalf("head=%q err=%v", head, err)
	}
	fh, err := res.File.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	all, err := io.ReadAll(fh)
	if err != nil || string(all) != "0123456789" {
		t.Fatalf("open=%q err=%v", all, err)
	}
}
