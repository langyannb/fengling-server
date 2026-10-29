package uc

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// recordedReq 记录假服务收到的请求，用来断言 header / query / body 的逐字形态。
type recordedReq struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     string
}

// fakeUC 起 httptest 假 UC 服务，并把 APIBase / CASBase / DriveBase / AuthPath /
// TmpDir 指向它和临时目录；用例结束自动还原，避免包级变量跨用例串味。
type fakeUC struct {
	Server *httptest.Server

	mu     sync.Mutex
	reqs   []recordedReq
	routes map[string]http.HandlerFunc
}

// withFakeUC 建立假服务并接管包级配置。
func withFakeUC(t *testing.T) *fakeUC {
	t.Helper()
	f := &fakeUC{routes: map[string]http.HandlerFunc{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedReq{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Header:   r.Header.Clone(),
			Body:     string(body),
		})
		f.mu.Unlock()
		h, ok := f.routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	oldAPI, oldCAS, oldDrive, oldAuth, oldTmp := APIBase, CASBase, DriveBase, AuthPath, TmpDir
	APIBase = f.Server.URL + "/api"
	CASBase = f.Server.URL + "/cas"
	DriveBase = f.Server.URL + "/drive"
	AuthPath = filepath.Join(t.TempDir(), "uc_auth.php")
	TmpDir = t.TempDir()
	t.Cleanup(func() {
		APIBase, CASBase, DriveBase, AuthPath, TmpDir = oldAPI, oldCAS, oldDrive, oldAuth, oldTmp
		f.Server.Close()
	})
	return f
}

// on 注册一条路由。
func (f *fakeUC) on(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[path] = h
}

// reply 注册一条固定状态码 + 固定 body 的路由。
func (f *fakeUC) reply(path string, status int, body string, setCookies ...string) {
	f.on(path, func(w http.ResponseWriter, _ *http.Request) {
		for _, c := range setCookies {
			w.Header().Add("Set-Cookie", c)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// reqsFor 取某个路径收到的全部请求（按到达顺序）。
func (f *fakeUC) reqsFor(path string) []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedReq, 0, len(f.reqs))
	for _, r := range f.reqs {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// all 取全部请求。
func (f *fakeUC) all() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedReq, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func mustEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}

// mustValue 断言 map 里的 any 值（动态类型必须也一致，比如 int64(0) != float64(0)）。
func mustValue(t *testing.T, label string, got, want any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %#v, want %#v", label, got, want)
	}
}
