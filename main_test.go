package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	if e := setPassword(dir, "test-password-long-2026"); e != nil {
		t.Fatal(e)
	}
	a, e := newApp(dir, "http://example.test")
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func request(a *App, method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.test")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	return w
}
func loginTest(t *testing.T, a *App) ([]*http.Cookie, string) {
	t.Helper()
	get := request(a, "GET", "/studio/login", nil, nil)
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(get.Body.String())
	if len(csrf) != 2 {
		t.Fatal("login form missing")
	}
	w := request(a, "POST", "/studio/login", url.Values{"csrf": {csrf[1]}, "password": {"test-password-long-2026"}}, get.Result().Cookies())
	if w.Code != 303 {
		t.Fatalf("login failed %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	admin := request(a, "GET", "/studio", nil, cookies)
	if admin.Code != 200 {
		t.Fatal("admin failed")
	}
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(admin.Body.String())
	if len(match) != 2 {
		t.Fatal("admin csrf missing")
	}
	return cookies, match[1]
}
func TestAccessAndMutations(t *testing.T) {
	a := testApp(t)
	w := request(a, "GET", "/", nil, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "/studio") {
		t.Fatal("public page exposes management")
	}
	if request(a, "GET", "/studio", nil, nil).Code != 303 {
		t.Fatal("anonymous admin")
	}
	if request(a, "POST", "/studio/action", url.Values{"action": {"category-add"}, "name": {"attack"}}, nil).Code != 401 {
		t.Fatal("anonymous write allowed")
	}
	cookies, csrf := loginTest(t, a)
	if request(a, "POST", "/studio/action", url.Values{"action": {"category-add"}, "name": {"unsafe"}}, cookies).Code != 403 {
		t.Fatal("csrf bypass")
	}
	v := url.Values{"action": {"category-edit"}, "id": {"landscape"}, "name": {"Новая категория"}, "csrf": {csrf}}
	if request(a, "POST", "/studio/action", v, cookies).Code != 303 {
		t.Fatal("rename failed")
	}
	reloaded, e := newApp(a.dir, "")
	if e != nil || reloaded.snapshot().Categories[0].Name != "Новая категория" {
		t.Fatal("rename not persisted")
	}
	v = url.Values{"action": {"category-delete"}, "id": {"landscape"}, "csrf": {csrf}}
	if request(a, "POST", "/studio/action", v, cookies).Code != 400 {
		t.Fatal("nonempty category deleted")
	}
	r := httptest.NewRequest("POST", "http://example.test/studio/action", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.test")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	blocked := httptest.NewRecorder()
	a.handler().ServeHTTP(blocked, r)
	if blocked.Code != 403 {
		t.Fatal("cross-origin action allowed")
	}
	if request(a, "POST", "/studio/action", url.Values{"action": {"logout"}, "csrf": {csrf}}, cookies).Code != 303 {
		t.Fatal("logout failed")
	}
	if request(a, "GET", "/studio", nil, cookies).Code != 303 {
		t.Fatal("logout session still valid")
	}
}
func TestUploadPersistenceAndDeletion(t *testing.T) {
	a := testApp(t)
	cookies, csrf := loginTest(t, a)
	var data bytes.Buffer
	mw := multipart.NewWriter(&data)
	mw.WriteField("csrf", csrf)
	mw.WriteField("action", "upload")
	mw.WriteField("category", "details")
	f, _ := mw.CreateFormFile("photos", "nature.png")
	img := image.NewRGBA(image.Rect(0, 0, 25, 30))
	img.Set(5, 6, color.RGBA{120, 210, 80, 255})
	png.Encode(f, img)
	mw.Close()
	r := httptest.NewRequest("POST", "http://example.test/studio/action", &data)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://example.test")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	photos := a.snapshot().Photos
	if len(photos) != 8 {
		t.Fatal("photo missing")
	}
	p := photos[7]
	if p.Demo || p.Category != "details" {
		t.Fatal("incorrect photo metadata")
	}
	if _, e := os.Stat(filepath.Join(a.dir, "uploads", p.ID+"-thumb.jpg")); e != nil {
		t.Fatal(e)
	}
	reload, e := newApp(a.dir, "")
	if e != nil || len(reload.snapshot().Photos) != 8 {
		t.Fatal("upload not persisted")
	}
	if request(a, "GET", p.URL, nil, nil).Code != 200 {
		t.Fatal("uploaded media unavailable")
	}
	request(a, "POST", "/studio/action", url.Values{"csrf": {csrf}, "action": {"hero"}, "id": {p.ID}}, cookies)
	if a.view().Hero.ID != p.ID {
		t.Fatal("hero not changed")
	}
	if request(a, "POST", "/studio/action", url.Values{"csrf": {csrf}, "action": {"photo-delete"}, "id": {p.ID}}, cookies).Code != 303 {
		t.Fatal("delete failed")
	}
	if request(a, "GET", p.URL, nil, nil).Code != 404 {
		t.Fatal("deleted image still served")
	}
	if _, e = os.Stat(filepath.Join(a.dir, "uploads", p.ID+".jpg")); !os.IsNotExist(e) {
		t.Fatal("deleted file retained")
	}
}
func TestRateLimitAndExpiredSession(t *testing.T) {
	a := testApp(t)
	get := request(a, "GET", "/studio/login", nil, nil)
	csrf := get.Result().Cookies()[0].Value
	v := url.Values{"csrf": {csrf}, "password": {"wrong"}}
	for i := 0; i < 5; i++ {
		if request(a, "POST", "/studio/login", v, get.Result().Cookies()).Code != 401 {
			t.Fatal("unexpected login response")
		}
	}
	if request(a, "POST", "/studio/login", v, get.Result().Cookies()).Code != 429 {
		t.Fatal("rate limit failed")
	}
	a.attempts = map[string]Attempt{}
	cookies, _ := loginTest(t, a)
	for id, s := range a.sessions {
		s.Expires = time.Now().Add(-time.Second)
		a.sessions[id] = s
	}
	if request(a, "GET", "/studio", nil, cookies).Code != 303 {
		t.Fatal("expired session accepted")
	}
}
func TestInvalidUploadAndEscaping(t *testing.T) {
	a := testApp(t)
	cookies, csrf := loginTest(t, a)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	mw.WriteField("csrf", csrf)
	mw.WriteField("action", "upload")
	mw.WriteField("category", "details")
	f, _ := mw.CreateFormFile("photos", "evil.svg")
	io.WriteString(f, `<svg onload="alert(1)"></svg>`)
	mw.Close()
	r := httptest.NewRequest("POST", "http://example.test/studio/action", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://example.test")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 400 || len(a.snapshot().Photos) != 7 {
		t.Fatal("invalid image accepted")
	}
	v := url.Values{"csrf": {csrf}, "action": {"category-edit"}, "id": {"landscape"}, "name": {`<script>alert(1)</script>`}}
	if request(a, "POST", "/studio/action", v, cookies).Code != 303 {
		t.Fatal("rename failed")
	}
	public := request(a, "GET", "/", nil, nil)
	if strings.Contains(public.Body.String(), `<script>alert(1)</script>`) {
		t.Fatal("unescaped content")
	}
	if public.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("CSP missing")
	}
}
